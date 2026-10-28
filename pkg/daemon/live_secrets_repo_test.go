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

// The secrets repository against a real GitHub remote, which is the one
// half of it nothing else in this repository can see.
//
// pkg/secretsrepo's own suite pushes, restores and resolves conflicts
// against bare repositories in a temp directory, and that is honest as
// far as it goes -- git treats a path as a remote exactly as it treats a
// URL, and what those tests pin is grain's rules about which copy wins,
// not git's ability to push. What a path on disk cannot be is
// authenticated: it never asks for a credential, so the whole ladder
// this deployment actually pushes through (a pattern in
// <data-dir>/secrets/github/credentials.json naming a token file, read
// per operation and handed to git through an askpass script) is
// exercised by nothing, and neither is what happens when the answer is
// no. Nor can it be somebody else's: a branch that does not exist yet on
// a repository that already holds things, a push landing on top of a
// commit another deployment made, a repository the credential does not
// cover.
//
// So these tests drive cmd/grain's own entry points --
// openSecretsRepo, syncSecretsRepo, startupSecretsRepoSync -- against a
// real private repository on github.com, and check what they did by
// cloning it back with git and reading the bytes. They cover the three
// claims docs/test-plans/2026-09-05.md section 3 says nobody has ever
// checked:
//
//   - the first sync to a repository with nothing of grain's in it: the
//     commit lands, the file is named for this deployment's state
//     repository, and its content is ciphertext sealed to this
//     deployment's key.
//   - the restore: a host with an empty data directory and the key it
//     was deployed with rebuilds the file from the repository and reads
//     a secret back out of it.
//   - two deployments sharing one repository: two files, neither
//     overwriting the other, and the second deployment's push landing on
//     top of the first's rather than being refused forever.
//
// # The credential, and why this is not in the pull-request suite
//
// Both gates below skip unless GRAIN_LIVE_SECRETS_REPO names a
// repository and GRAIN_LIVE_SECRETS_REPO_TOKEN holds a credential for
// it, so `go test ./...` on a laptop and on a pull request stays a unit
// run and costs nothing. A skip and a pass are the same `ok` in go
// test's summary, so GRAIN_LIVE_SECRETS_REPO_TEST turns both gates into
// failures -- .github/workflows/live-secrets-repo.yml sets it, and a
// maintainer running this by hand should too:
//
//	GRAIN_LIVE_SECRETS_REPO_TEST=1 \
//	GRAIN_LIVE_SECRETS_REPO=https://github.com/<owner>/<private-repo> \
//	GRAIN_LIVE_SECRETS_REPO_TOKEN=<token with contents:write on it> \
//	  go test ./cmd/grain/ -run TestLiveSecretsRepo -count=1 -v
//
// That workflow is nightly and holds its token in an environment of its
// own, for the reasons live-agent.yml's header gives about a model
// credential and tests/deploy's TestOnlyTheScheduledLiveJobHoldsAModelCredential
// enforces: a repository secret is readable by any workflow on any
// branch push, and in this repository most branches are pushed by
// grain's own agents from runs no human has read.
//
// # What it does to the repository it is pointed at
//
// Every test works on a branch of its own, named for the moment it
// started, and deletes it on the way out -- so a run leaves the
// repository as it found it, two runs never collide, and the branch each
// one starts on is genuinely a branch the remote does not have yet
// (pkg/secretsrepo's fetch, which starts it). A crashed run's leftovers
// are swept by the next one, a day later, which is late enough that a
// run still going is never swept out from under itself.
//
// The repository must therefore be one nothing else keeps anything in:
// give this its own private repository and a token scoped to that
// repository alone. Create it with a README rather than empty -- GitHub
// makes the first branch pushed to an empty repository its default
// branch, and a default branch cannot be deleted, so a run against an
// empty one leaves a branch behind (and says so in its log). Nothing is
// lost by that: the empty repository is the same code path as a branch
// the remote does not have yet, pkg/secretsrepo's own suite covers it
// against a bare remote, and what only github.com can add is the
// credential and somebody else's writes.
//
// It is never read for anything but grain's own
// ciphertext and it is never the deployment's state repository -- the
// daemon refuses that configuration outright (refuseStateRepoAsSecretsRepo),
// which is why the state repository named below is a URL this test never
// contacts.

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/secretsrepo"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

const (
	// liveRemoteEnv names the private repository to push to; the token
	// that reaches it is liveTokenEnv, and liveRequireEnv turns a missing
	// one from a skip into a failure.
	liveRemoteEnv  = "GRAIN_LIVE_SECRETS_REPO"
	liveTokenEnv   = "GRAIN_LIVE_SECRETS_REPO_TOKEN"
	liveRequireEnv = "GRAIN_LIVE_SECRETS_REPO_TEST"

	// liveBranchPrefix marks every branch these tests create, so the
	// sweep can tell its own leftovers from anything else in the
	// repository and never delete something a person put there.
	liveBranchPrefix = "grain-live-test-"
	// liveBranchStamp is the moment in a branch's name, which is what
	// makes a leftover's age readable without asking GitHub about it.
	liveBranchStamp = "20060102T150405Z"
	// liveSweepAfter is how old a leftover branch must be before a run
	// deletes it: long enough that a run still in progress, or one a
	// maintainer is watching, is never swept away under it.
	liveSweepAfter = 24 * time.Hour

	// liveCredentialName is what the credential is called in the ladder
	// this test writes -- a name of its own rather than the deployment
	// default, because that is the shape an operator giving the secrets
	// repository its own narrowly scoped token ends up with.
	liveCredentialName = "live-secrets-repo"

	// sandboxRepoPath and sandboxTokenPath are where the github-sandbox
	// capability places the repository name and token.
	sandboxRepoPath  = "/home/debian/.github-sandbox-repo"
	sandboxTokenPath = "/home/debian/.github-sandbox-token"
)

// resolveLiveSecretsRepo resolves the git remote URL and token from either
// environment variables or placed capability files.
func resolveLiveSecretsRepo(envRepo, envToken, repoPath, tokenPath string) (remote, token string) {
	remote = strings.TrimSpace(envRepo)
	if remote == "" {
		remote = strings.TrimSpace(readIfPresent(repoPath))
	}
	token = strings.TrimSpace(envToken)
	if token == "" {
		token = strings.TrimSpace(readIfPresent(tokenPath))
	}
	if !strings.HasPrefix(remote, "http") {
		if !strings.Contains(remote, ":") && strings.Contains(remote, "/") {
			remote = "https://github.com/" + remote
		}
	}
	return remote, token
}

// liveSecretsRepo is the gate: the repository and the credential, or a
// skip that says which of the two is missing.
func liveSecretsRepo(t *testing.T) (remote, token string) {
	t.Helper()
	remote, token = resolveLiveSecretsRepo(
		os.Getenv(liveRemoteEnv),
		os.Getenv(liveTokenEnv),
		sandboxRepoPath,
		sandboxTokenPath,
	)
	if remote == "" {
		skipUnlessLiveRepoRequired(t, liveRemoteEnv+" names no repository and "+sandboxRepoPath+" is absent")
	}
	if token == "" {
		skipUnlessLiveRepoRequired(t, liveTokenEnv+" is not set and "+sandboxTokenPath+" is absent")
	}
	if !strings.HasPrefix(remote, "http") {
		// An ssh remote would authenticate through a key on the host
		// rather than through the ladder, which is the half this file
		// exists to prove.
		t.Fatalf("%s must be an https URL, so the push goes through grain's credential ladder; got %q",
			liveRemoteEnv, remote)
	}
	if _, err := exec.LookPath("git"); err != nil {
		skipUnlessLiveRepoRequired(t, "git is not installed")
	}
	return remote, token
}

func readIfPresent(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// skipUnlessLiveRepoRequired skips, unless something has declared that
// this run was supposed to be a live one -- in which case the missing
// credential is the failure, since a silent skip is indistinguishable
// from a pass in go test's summary.
func skipUnlessLiveRepoRequired(t *testing.T, reason string) {
	t.Helper()
	if os.Getenv(liveRequireEnv) != "" {
		t.Fatalf("%s is set, so this test may not skip: %s", liveRequireEnv, reason)
	}
	t.Skipf("%s; set %s and %s to run this against a real GitHub repository", reason, liveRemoteEnv, liveTokenEnv)
}

// liveDeployment is one grain: a data directory configured the way a
// deploy leaves one -- a state repository it is named after, a secrets
// repository to back up to, and a credential ladder that covers it.
type liveDeployment struct {
	dataDir     string
	stateRemote string
	remote      string
	branch      string
	token       string
}

func newLiveDeployment(t *testing.T, remote, token, branch, stateRemote string) *liveDeployment {
	t.Helper()
	d := &liveDeployment{
		dataDir: t.TempDir(), stateRemote: stateRemote,
		remote: remote, branch: branch, token: token,
	}
	// The state repository is named but never contacted: openSecretsRepo
	// reads it to derive the file name and to refuse the two being one
	// repository, and nothing here clones it.
	if err := staterepo.SaveSettings(d.dataDir, staterepo.Settings{Remote: stateRemote}); err != nil {
		t.Fatal(err)
	}
	if err := secretsrepo.SaveSettings(d.dataDir, secretsrepo.Settings{Remote: remote, Branch: branch}); err != nil {
		t.Fatal(err)
	}
	d.writeCredentialLadder(t, remote)
	return d
}

// writeCredentialLadder puts the token where the daemon's own push looks
// for it: a credentials.json pattern for this repository naming a
// credential, and that credential's material in <name>.token. This is
// repoToken's path, and the reason the token in these tests is never in
// an environment variable git could have picked up by itself.
func (d *liveDeployment) writeCredentialLadder(t *testing.T, remote string) {
	t.Helper()
	owner, name, ok := repoFromRemote(remote)
	if !ok {
		t.Fatalf("%s is not a repository URL this deployment can name a credential for: %q", liveRemoteEnv, remote)
	}
	dir := filepath.Join(d.dataDir, "secrets", "github")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	pattern := strings.ToLower(owner + "/" + name)
	writeLiveFile(t, filepath.Join(dir, "credentials.json"),
		fmt.Sprintf("{%q: %q}\n", pattern, liveCredentialName))
	writeLiveFile(t, filepath.Join(dir, liveCredentialName+".token"), d.token+"\n")
}

// secretsFile is where this deployment's ciphertext lives on its own
// disk -- the copy that always wins, and the one a restore writes.
func (d *liveDeployment) secretsFile() string { return SecretsConfig(d.dataDir).File }

// setSecret writes a secret through the real store, so what reaches the
// repository is ciphertext this deployment's key made rather than a
// string a test called ciphertext.
func (d *liveDeployment) setSecret(t *testing.T, key, value string) *secrets.Store {
	t.Helper()
	store := secrets.Open(SecretsConfig(d.dataDir))
	if err := store.Set("live-secrets-repo", key, []byte(value)); err != nil {
		t.Fatalf("writing a secret: %v", err)
	}
	return store
}

// sync is one reconcile through the daemon's own opener, with what it
// did reported -- openSecretsRepo rather than syncSecretsRepo only
// because the Result is what these tests assert on.
func (d *liveDeployment) sync(t *testing.T) secretsrepo.Result {
	t.Helper()
	repo, err := openSecretsRepo(context.Background(), d.dataDir)
	if err != nil {
		t.Fatalf("opening the secrets repository: %v", err)
	}
	out, err := repo.Sync(context.Background())
	if err != nil {
		t.Fatalf("syncing with %s: %v", d.remote, err)
	}
	return out
}

// The first sync: a real private repository, a branch it does not have
// yet, and a deployment whose ciphertext has never left its disk.
func TestLiveSecretsRepoFirstSyncLandsCiphertextOnGitHub(t *testing.T) {
	t.Parallel()
	remote, token := liveSecretsRepo(t)
	branch := liveBranch(t, remote, token)

	d := newLiveDeployment(t, remote, token, branch, "https://github.com/acme/grain-state")
	canary := "the live suite wrote this on " + branch
	store := d.setSecret(t, "canary", canary)

	if out := d.sync(t); !out.Committed || !out.Pushed {
		t.Fatalf("the first sync to %s did not commit and push: %+v", remote, out)
	}

	// What another clone gets, which is the only thing worth asserting:
	// the file is there, under the name this deployment's state
	// repository gives it.
	clone := liveClone(t, remote, token, branch)
	want := "grain-acme-grain-state.secrets.enc"
	if want != secretsrepo.FileName("https://github.com/acme/grain-state") {
		t.Fatalf("this test's own idea of the file name has drifted from FileName's: %q", want)
	}
	stored, err := os.ReadFile(filepath.Join(clone, want))
	if err != nil {
		t.Fatalf("%s is not in %s on branch %s: %v\nthe branch holds: %s",
			want, remote, branch, err, liveTree(t, clone))
	}
	onDisk, err := os.ReadFile(d.secretsFile())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, onDisk) {
		t.Errorf("what landed on %s is not this host's file", remote)
	}

	// And it is ciphertext: the secret is not in it in any readable
	// form, and what is in it is sealed to this deployment's own key --
	// the property that makes pushing it anywhere at all defensible.
	if bytes.Contains(stored, []byte(canary)) || bytes.Contains(stored, []byte("canary")) {
		t.Errorf("the file pushed to %s has the plaintext secret in it", remote)
	}
	recipient, err := secrets.Recipient(stored)
	if err != nil {
		t.Fatalf("what landed on %s is not a grain-encrypted file: %v", remote, err)
	}
	public, err := store.PublicKey()
	if err != nil {
		t.Fatal(err)
	}
	if recipient != public {
		t.Errorf("the file is sealed to %s, not to this deployment's key %s", recipient, public)
	}

	// A second sync with nothing changed must be silent: this runs on a
	// timer against a real repository, and a commit per tick would be a
	// history nobody can read and a push every thirty seconds forever.
	if again := d.sync(t); again.Committed || again.Pushed {
		t.Errorf("a sync with nothing to carry pushed anyway: %+v", again)
	}
}

// The case the whole package exists for, done for real: the host is
// gone, its data directory is empty, and the only thing it was given
// back by hand is the key. The ciphertext has to come down from GitHub
// on its own and open.
func TestLiveSecretsRepoRestoresARebuiltHost(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	remote, token := liveSecretsRepo(t)
	branch := liveBranch(t, remote, token)
	stateRemote := "https://github.com/acme/grain-state"

	first := newLiveDeployment(t, remote, token, branch, stateRemote)
	canary := "restored through " + remote
	first.setSecret(t, "canary", canary)
	if out := first.sync(t); !out.Pushed {
		t.Fatalf("the deployment being rebuilt never pushed: %+v", out)
	}
	key, err := secrets.ReadKeyFile(SecretsConfig(first.dataDir).KeyFile)
	if err != nil {
		t.Fatalf("reading the key this deployment was running on: %v", err)
	}

	// The rebuilt host: a fresh data directory, the same two settings
	// files a redeploy writes, and the key handed over the way every
	// deploy path hands one over -- written before anything opens the
	// store, because a store that mints itself a key first is a host
	// holding a key that opens nothing.
	rebuilt := newLiveDeployment(t, remote, token, branch, stateRemote)
	if err := os.MkdirAll(SecretsDir(rebuilt.dataDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := secrets.WriteKeyFile(SecretsConfig(rebuilt.dataDir).KeyFile, key); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(rebuilt.secretsFile()); !os.IsNotExist(err) {
		t.Fatalf("the rebuilt host already has a secrets file: %v", err)
	}

	if repo := StartupSecretsRepoSync(ctx, rebuilt.dataDir); repo == nil {
		t.Fatal("a configured deployment opened no secrets repository at startup")
	}
	info, err := os.Stat(rebuilt.secretsFile())
	if err != nil {
		t.Fatalf("nothing was restored from %s: %v", remote, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("the restored file is mode %v, not 0600", perm)
	}

	// And it opens with the key it was deployed with, which is the whole
	// claim: not that a file came back, but that this host can read it.
	store := OpenSecrets(rebuilt.dataDir)
	if err := store.Check(); err != nil {
		t.Fatalf("the restored file cannot be read by the key this host was given: %v", err)
	}
	got, err := store.Resolve(ctx, "live-secrets-repo/canary")
	if err != nil {
		t.Fatalf("resolving a secret out of the restored file: %v", err)
	}
	if got != canary {
		t.Errorf("the restored secret is %q, want %q", got, canary)
	}
}

// Two deployments, one repository, which is what an operator running
// staging and production gets when they point both at the same private
// repository. Neither may lose the other's file, and neither may end up
// stuck: the second deployment's push happens after a commit it has
// never seen, which is a non-fast-forward GitHub itself refuses unless
// the fetch has reset onto it first.
func TestLiveSecretsRepoTwoDeploymentsShareOneRepository(t *testing.T) {
	t.Parallel()
	remote, token := liveSecretsRepo(t)
	branch := liveBranch(t, remote, token)

	staging := newLiveDeployment(t, remote, token, branch, "https://github.com/acme/staging-state")
	staging.setSecret(t, "canary", "staging's secret")
	if out := staging.sync(t); !out.Pushed {
		t.Fatalf("staging did not push: %+v", out)
	}

	production := newLiveDeployment(t, remote, token, branch, "https://github.com/acme/production-state")
	production.setSecret(t, "canary", "production's secret")
	if out := production.sync(t); !out.Pushed {
		t.Fatalf("production could not push onto staging's commit: %+v", out)
	}

	stagingFile := secretsrepo.FileName("https://github.com/acme/staging-state")
	productionFile := secretsrepo.FileName("https://github.com/acme/production-state")
	clone := liveClone(t, remote, token, branch)
	for _, d := range []struct {
		name string
		file string
		host *liveDeployment
	}{{"staging", stagingFile, staging}, {"production", productionFile, production}} {
		stored, err := os.ReadFile(filepath.Join(clone, d.file))
		if err != nil {
			t.Fatalf("%s's file is not on %s: %v\nthe branch holds: %s", d.name, remote, err, liveTree(t, clone))
		}
		onDisk, err := os.ReadFile(d.host.secretsFile())
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(stored, onDisk) {
			t.Errorf("%s's file on %s is not %s's ciphertext", d.name, remote, d.name)
		}
	}

	// And staging goes on working afterwards: its next rotation has to
	// land on top of production's commit rather than be refused as a
	// non-fast-forward for as long as the deployment lives.
	staging.setSecret(t, "canary", "staging's rotated secret")
	if out := staging.sync(t); !out.Pushed {
		t.Fatalf("staging could not push after production moved the branch: %+v", out)
	}
	clone = liveClone(t, remote, token, branch)
	rotated, err := os.ReadFile(filepath.Join(clone, stagingFile))
	if err != nil {
		t.Fatal(err)
	}
	onDisk, err := os.ReadFile(staging.secretsFile())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rotated, onDisk) {
		t.Errorf("staging's rotation did not reach %s", remote)
	}
	if _, err := os.ReadFile(filepath.Join(clone, productionFile)); err != nil {
		t.Errorf("staging's push lost production's file: %v", err)
	}
}

// A repository the credential does not reach -- which is what a token
// scoped to the wrong repository, an org that requires SSO, or a URL
// with a typo in it all look like from here. The push must fail, and say
// so: git prompting for a password on a host with no terminal would wedge
// the daemon's sync timer instead (GIT_TERMINAL_PROMPT=0, in
// pkg/secretsrepo's gitAuthed), and nothing but a real remote asking for
// credentials can tell the two apart.
func TestLiveSecretsRepoARepositoryTheCredentialCannotReachFails(t *testing.T) {
	t.Parallel()
	remote, token := liveSecretsRepo(t)
	missing := remote + "-no-such-repository"

	d := newLiveDeployment(t, missing, token, secretsrepo.DefaultBranch, "https://github.com/acme/grain-state")
	d.setSecret(t, "canary", "never leaves this host")

	done := make(chan error, 1)
	go func() {
		_, err := SyncSecretsRepo(context.Background(), d.dataDir)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("syncing with %s, which this credential does not cover, reported success", missing)
		}
		if strings.Contains(err.Error(), token) {
			t.Errorf("the failure quotes the credential back: %v", err)
		}
		t.Logf("a repository the credential cannot reach failed with: %v", err)
	case <-time.After(4 * time.Minute):
		// pkg/secretsrepo caps every git call at two minutes, so this is
		// past even a clone that hung and was killed.
		t.Fatal("syncing with an unreachable repository never returned: git is waiting for a credential nobody can type")
	}
}

// liveBranch is this run's own branch: named for the moment it started
// so a leftover's age can be read off it, deleted when the test ends,
// and swept a day later if the run that made it never got that far.
//
// A branch of its own per test rather than the repository's default
// branch, for three reasons. It is the shape of the case worth proving
// -- a branch the remote does not have yet. It leaves the repository as
// it found it, which is what makes a nightly safe to run against a
// repository an operator also uses. And two runs, or a run and a
// maintainer's own, never write the same ref.
func liveBranch(t *testing.T, remote, token string) string {
	t.Helper()
	branch := liveBranchPrefix + time.Now().UTC().Format(liveBranchStamp) +
		fmt.Sprintf("-%04x", rand.Intn(1<<16))
	t.Cleanup(func() {
		deleteLiveBranch(t, remote, token, branch)
		sweepLiveBranches(t, remote, token)
	})
	return branch
}

func deleteLiveBranch(t *testing.T, remote, token, branch string) {
	t.Helper()
	if out, err := liveGit(t, token, "", "push", "--quiet", remote, "--delete", branch); err != nil {
		// Not a failure of the test: the branch may never have been
		// pushed, and GitHub refuses to delete the repository's default
		// branch -- which is what the first branch pushed to an empty
		// repository becomes. Said out loud, because the alternative is a
		// repository quietly filling with this suite's leftovers.
		t.Logf("could not delete %s from %s (delete it by hand if it is still there): %v: %s",
			branch, remote, err, out)
	}
}

// sweepLiveBranches deletes what an interrupted run left behind, and
// only that: branches this suite named, older than liveSweepAfter, so a
// run in progress is never swept out from under itself.
func sweepLiveBranches(t *testing.T, remote, token string) {
	t.Helper()
	out, err := liveGit(t, token, "", "ls-remote", "--heads", remote, "refs/heads/"+liveBranchPrefix+"*")
	if err != nil {
		t.Logf("could not list %s to sweep old test branches: %v: %s", remote, err, out)
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		_, ref, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok {
			continue
		}
		branch := strings.TrimPrefix(ref, "refs/heads/")
		stamp, _, _ := strings.Cut(strings.TrimPrefix(branch, liveBranchPrefix), "-")
		started, err := time.Parse(liveBranchStamp, stamp)
		if err != nil || time.Since(started) < liveSweepAfter {
			continue
		}
		t.Logf("sweeping %s, left behind by a run on %s", branch, started.Format(time.RFC3339))
		deleteLiveBranch(t, remote, token, branch)
	}
}

// liveClone is what somebody else sees: a fresh clone of the branch,
// made by git rather than by anything in this repository, which is the
// only way to assert on what actually reached GitHub.
func liveClone(t *testing.T, remote, token, branch string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "clone")
	if out, err := liveGit(t, token, "", "clone", "--quiet", "--branch", branch, remote, dir); err != nil {
		t.Fatalf("cloning %s at %s back: %v: %s", remote, branch, err, out)
	}
	return dir
}

// liveTree lists what a clone holds, for a failure message that says
// what is on the branch rather than only what is missing from it.
func liveTree(t *testing.T, dir string) string {
	t.Helper()
	out, err := liveGit(t, "", dir, "ls-tree", "--name-only", "HEAD")
	if err != nil {
		return "(could not be listed: " + err.Error() + ")"
	}
	return strings.Join(strings.Fields(out), " ")
}

// liveGit runs git with the live credential the way pkg/secretsrepo's
// own gitAuthed does: through an askpass script, so the token is in no
// argv where `ps` would read it and in no URL where .git/config would
// keep it.
func liveGit(t *testing.T, token, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if token != "" {
		script := filepath.Join(t.TempDir(), "askpass.sh")
		writeLiveFile(t, script, "#!/bin/sh\ncase \"$1\" in\n*[Uu]sername*) printf '%s' 'x-access-token' ;;\n*) "+
			"printf '%s' '"+strings.ReplaceAll(token, "'", `'\''`)+"' ;;\nesac\n")
		if err := os.Chmod(script, 0o700); err != nil {
			t.Fatal(err)
		}
		cmd.Env = append(cmd.Env, "GIT_ASKPASS="+script)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func writeLiveFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestResolveLiveSecretsRepoFallback(t *testing.T) {
	dir := t.TempDir()
	repoFile := filepath.Join(dir, "repo")
	tokenFile := filepath.Join(dir, "token")

	// 1. Env vars take precedence when set.
	if err := os.WriteFile(repoFile, []byte("file-owner/file-repo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenFile, []byte("file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gotRemote, gotToken := resolveLiveSecretsRepo("https://github.com/env-owner/env-repo", "env-token", repoFile, tokenFile)
	if gotRemote != "https://github.com/env-owner/env-repo" || gotToken != "env-token" {
		t.Fatalf("expected env vars, got remote %q, token %q", gotRemote, gotToken)
	}

	// 2. Falls back to files with owner/repo format.
	gotRemote, gotToken = resolveLiveSecretsRepo("", "", repoFile, tokenFile)
	if gotRemote != "https://github.com/file-owner/file-repo" || gotToken != "file-token" {
		t.Fatalf("expected fallback from files, got remote %q, token %q", gotRemote, gotToken)
	}

	// 3. Falls back to files with https URL format.
	if err := os.WriteFile(repoFile, []byte("https://github.com/file-owner/file-repo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gotRemote, gotToken = resolveLiveSecretsRepo("", "", repoFile, tokenFile)
	if gotRemote != "https://github.com/file-owner/file-repo" || gotToken != "file-token" {
		t.Fatalf("expected fallback from files with https URL, got remote %q, token %q", gotRemote, gotToken)
	}

	// 4. Missing files return empty strings.
	nonexistentRepo := filepath.Join(dir, "no-such-repo")
	nonexistentToken := filepath.Join(dir, "no-such-token")
	gotRemote, gotToken = resolveLiveSecretsRepo("", "", nonexistentRepo, nonexistentToken)
	if gotRemote != "" || gotToken != "" {
		t.Fatalf("expected empty strings for nonexistent files, got remote %q, token %q", gotRemote, gotToken)
	}
}
