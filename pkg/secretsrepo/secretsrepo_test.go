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

package secretsrepo_test

// What a secrets repository is for, in the two directions it carries
// anything: this host's ciphertext reaching a remote it can be restored
// from, and a data directory with no ciphertext in it getting one back.
//
// The tests run against a bare repository in a temp directory rather
// than a network remote -- git treats a path as a remote exactly as it
// treats a URL, and what is being pinned here is grain's own rules about
// which copy wins, not git's ability to push.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/secretsrepo"
)

// bareRemote is the remote every test here pushes to: a real bare
// repository in a temporary directory, on main.
//
// The two config settings are what keep a push from outliving the test
// that made it: git runs auto-maintenance on the receiving side of a
// push, and since v2.47 it detaches, so the repacking goes on in the
// background while t.TempDir deletes the tree underneath it, and the
// deletion fails on files that land after RemoveAll has walked past them
// ("TempDir RemoveAll cleanup: unlinkat ...: directory not empty").
// Saying autoDetach=false does not turn the maintenance off, it runs it
// in the foreground, so the push that triggered it is what waits for it.
// pkg/cli/bare_remote_test.go's initBareRepo has the whole
// explanation, including why both keys are set and why nothing on a
// runner with git 2.39 can see any of this; this is that helper
// restated, because this codebase duplicates a small test helper across
// packages rather than sharing one (tests/e2e/harness_test.go's own
// comment on why).
func bareRemote(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "secrets.git")
	if out, err := exec.Command("git", "init", "--bare", "--initial-branch=main", dir).CombinedOutput(); err != nil {
		t.Fatalf("creating a bare remote: %v: %s", err, out)
	}
	for _, kv := range [][2]string{
		{"maintenance.autoDetach", "false"},
		{"gc.autoDetach", "false"},
	} {
		cmd := exec.Command("git", "config", kv[0], kv[1])
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setting %s on the bare remote: %v: %s", kv[0], err, out)
		}
	}
	return dir
}

// host is one deployment: a data directory holding the encrypted file,
// and a working tree for the secrets repository beside it.
type host struct {
	dataDir string
	source  string
	repo    *secretsrepo.Repo
}

func newHost(t *testing.T, remote, file string) *host {
	t.Helper()
	return newHostOnBranch(t, remote, "", file)
}

// newHostOnBranch is newHost for a deployment configured for a branch of
// its own, which is what Settings.Branch is; "" is DefaultBranch.
func newHostOnBranch(t *testing.T, remote, branch, file string) *host {
	t.Helper()
	dataDir := t.TempDir()
	source := filepath.Join(dataDir, "secrets", "secrets.enc")
	repo, err := secretsrepo.Open(context.Background(), secretsrepo.Config{
		Dir:    filepath.Join(dataDir, "secrets-repo"),
		Remote: remote,
		Branch: branch,
		File:   file,
		Source: source,
	})
	if err != nil {
		t.Fatalf("opening the secrets repository: %v", err)
	}
	return &host{dataDir: dataDir, source: source, repo: repo}
}

func (h *host) write(t *testing.T, ciphertext string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(h.source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.source, []byte(ciphertext), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (h *host) sync(t *testing.T) secretsrepo.Result {
	t.Helper()
	out, err := h.repo.Sync(context.Background())
	if err != nil {
		t.Fatalf("syncing: %v", err)
	}
	return out
}

func TestSyncPushesTheCiphertextToTheRemote(t *testing.T) {
	remote := bareRemote(t)
	h := newHost(t, remote, "grain-owner-state.secrets.enc")
	h.write(t, "sealed-to-this-host")

	out := h.sync(t)
	if !out.Committed || !out.Pushed {
		t.Fatalf("the first sync did not commit and push: %+v", out)
	}

	got := showRemote(t, remote, "grain-owner-state.secrets.enc")
	if got != "sealed-to-this-host" {
		t.Errorf("the remote holds %q, not the ciphertext this host wrote", got)
	}

	// A second sync with nothing changed must be silent: this runs on a
	// timer, and a commit per tick would be a repository nobody can read
	// and a push every thirty seconds forever.
	if again := h.sync(t); again.Committed || again.Pushed {
		t.Errorf("a sync with nothing to carry did something anyway: %+v", again)
	}
}

// The case the package exists for: the host is rebuilt, its data
// directory is empty, and its key is handed back by whatever the deploy
// path does with keys. The ciphertext has to come back on its own.
func TestSyncRestoresACiphertextThisHostNoLongerHas(t *testing.T) {
	remote := bareRemote(t)
	first := newHost(t, remote, "grain-owner-state.secrets.enc")
	first.write(t, "every secret grain has")
	first.sync(t)

	rebuilt := newHost(t, remote, "grain-owner-state.secrets.enc")
	out := rebuilt.sync(t)
	if !out.Restored {
		t.Fatalf("a host with no secrets file did not restore one: %+v", out)
	}
	data, err := os.ReadFile(rebuilt.source)
	if err != nil {
		t.Fatalf("reading the restored file: %v", err)
	}
	if string(data) != "every secret grain has" {
		t.Errorf("restored %q", data)
	}
	if info, err := os.Stat(rebuilt.source); err != nil {
		t.Fatal(err)
	} else if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("the restored file is mode %v, not 0600", perm)
	}
}

// The live file wins. A repository holding an older -- or somebody
// else's -- copy under this deployment's name must never be written over
// the secrets grain is actually running on.
func TestTheFileOnThisHostWinsOverTheOneInTheRepository(t *testing.T) {
	remote := bareRemote(t)
	first := newHost(t, remote, "grain-owner-state.secrets.enc")
	first.write(t, "what the repository holds")
	first.sync(t)

	second := newHost(t, remote, "grain-owner-state.secrets.enc")
	second.write(t, "what this host is running on")
	out := second.sync(t)
	if out.Restored {
		t.Error("a host with its own secrets file reported a restore")
	}
	data, err := os.ReadFile(second.source)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "what this host is running on" {
		t.Fatalf("the live secrets file was overwritten with %q", data)
	}
	if got := showRemote(t, remote, "grain-owner-state.secrets.enc"); got != "what this host is running on" {
		t.Errorf("the remote was not brought up to this host: %q", got)
	}
}

// Two deployments sharing one secrets repository. Neither may lose the
// other's file, and neither may end up stuck: the second host's push
// happens after the first host's commit it has never seen, which is a
// non-fast-forward unless the fetch has reset it onto that commit first.
func TestTwoDeploymentsShareOneRepository(t *testing.T) {
	remote := bareRemote(t)
	staging := newHost(t, remote, secretsrepo.FileName("https://github.com/owner/staging-state"))
	production := newHost(t, remote, secretsrepo.FileName("https://github.com/owner/production-state"))

	staging.write(t, "staging's secrets")
	staging.sync(t)
	production.write(t, "production's secrets")
	production.sync(t)

	if got := showRemote(t, remote, "grain-owner-staging-state.secrets.enc"); got != "staging's secrets" {
		t.Errorf("staging's file is %q after production pushed", got)
	}
	if got := showRemote(t, remote, "grain-owner-production-state.secrets.enc"); got != "production's secrets" {
		t.Errorf("production's file is %q", got)
	}

	// And staging goes on working afterwards: its next change has to
	// reach the remote on top of production's commit rather than be
	// refused as a non-fast-forward forever.
	staging.write(t, "staging's rotated secrets")
	if out := staging.sync(t); !out.Pushed {
		t.Fatalf("staging could not push after production moved the branch: %+v", out)
	}
	if got := showRemote(t, remote, "grain-owner-staging-state.secrets.enc"); got != "staging's rotated secrets" {
		t.Errorf("staging's rotation did not reach the remote: %q", got)
	}
	if got := showRemote(t, remote, "grain-owner-production-state.secrets.enc"); got != "production's secrets" {
		t.Errorf("staging's push lost production's file: %q", got)
	}
}

// A branch the remote does not have yet, in a repository that is not
// empty -- which is what an operator has as soon as they create the
// private repository with a README, or point a second deployment at a
// branch of its own.
//
// The clone lands on the repository's default branch, so nothing here
// works unless the configured branch is started explicitly: the commit
// would be made on the default branch and `git push origin <branch>`
// would answer "src refspec does not match any", leaving a deployment
// whose backup silently never happens. The empty repository is the
// happier half of the same case and is covered by every other test in
// this file.
func TestABranchTheRemoteDoesNotHaveYet(t *testing.T) {
	remote := bareRemote(t)
	seed(t, remote, "README.md", "somewhere an operator already keeps encrypted things\n")

	h := newHostOnBranch(t, remote, "grain-secrets", "grain-owner-state.secrets.enc")
	h.write(t, "sealed-to-this-host")
	if out := h.sync(t); !out.Committed || !out.Pushed {
		t.Fatalf("the first sync onto a branch the remote lacks did not commit and push: %+v", out)
	}
	if got := showRemoteOn(t, remote, "grain-secrets", "grain-owner-state.secrets.enc"); got != "sealed-to-this-host" {
		t.Errorf("the branch holds %q, not the ciphertext this host wrote", got)
	}
	// The repository's own default branch is left exactly as it was: this
	// deployment writes one file on one branch and touches nothing else
	// in a repository it is a guest in.
	if got := showRemoteOn(t, remote, "main", "README.md"); got != "somewhere an operator already keeps encrypted things\n" {
		t.Errorf("the default branch now holds %q", got)
	}

	// And the ordinary cycle still works once the branch exists: the next
	// change fetches it, resets onto it and pushes.
	h.write(t, "rotated")
	if out := h.sync(t); !out.Pushed {
		t.Fatalf("a later sync onto the branch it created did not push: %+v", out)
	}
	if got := showRemoteOn(t, remote, "grain-secrets", "grain-owner-state.secrets.enc"); got != "rotated" {
		t.Errorf("the branch holds %q after a rotation", got)
	}
}

// A push that failed has to be retried on a later tick even though
// nothing has changed since -- otherwise a deployment whose secrets
// stopped moving sits with an unpushed commit indefinitely, which is the
// backup silently not being one.
func TestACommitWhosePushFailedIsPushedLater(t *testing.T) {
	remote := bareRemote(t)
	h := newHost(t, remote, "grain-owner-state.secrets.enc")
	h.write(t, "written while the remote was away")

	gone := remote + ".gone"
	if err := os.Rename(remote, gone); err != nil {
		t.Fatal(err)
	}
	out, err := h.repo.Sync(context.Background())
	if err == nil {
		t.Fatal("a sync that could not reach the remote reported success")
	}
	if !out.Committed {
		t.Fatalf("the commit was not made before the push failed: %+v", out)
	}
	if err := os.Rename(gone, remote); err != nil {
		t.Fatal(err)
	}
	if again := h.sync(t); !again.Pushed {
		t.Fatalf("the unpushed commit was not carried out on a later sync: %+v", again)
	}
	if got := showRemote(t, remote, "grain-owner-state.secrets.enc"); got != "written while the remote was away" {
		t.Errorf("the remote holds %q", got)
	}
}

// A deployment that has never set a secret has nothing to push, and must
// not commit an empty file to say so.
func TestNothingOnEitherSideCommitsNothing(t *testing.T) {
	remote := bareRemote(t)
	h := newHost(t, remote, "grain-owner-state.secrets.enc")
	out := h.sync(t)
	if out.Committed || out.Pushed || out.Restored {
		t.Errorf("a deployment with no secrets did something: %+v", out)
	}
}

// No remote is not a failure to report every thirty seconds; it is the
// deployment that has not configured one.
func TestOpenWithoutARemoteIsASentinel(t *testing.T) {
	_, err := secretsrepo.Open(context.Background(), secretsrepo.Config{
		Dir: t.TempDir(), File: "grain-owner-state.secrets.enc", Source: "/tmp/secrets.enc",
	})
	if !errors.Is(err, secretsrepo.ErrNoRemote) {
		t.Fatalf("opening without a remote: %v", err)
	}
}

// Settings.File is free text an operator writes, and the one thing it
// must not be able to name is a file outside the working tree.
func TestOpenRefusesAFileOutsideTheRepository(t *testing.T) {
	for _, file := range []string{"../escaped.enc", "/etc/grain.enc"} {
		_, err := secretsrepo.Open(context.Background(), secretsrepo.Config{
			Dir: t.TempDir(), Remote: bareRemote(t), File: file, Source: "/tmp/secrets.enc",
		})
		if err == nil || !strings.Contains(err.Error(), "outside the repository") {
			t.Errorf("opening with file %q: %v", file, err)
		}
	}
}

func TestSyncPushesToMainWhenRemoteOnlyHasSuffixMatchingBranch(t *testing.T) {
	remote := bareRemote(t)
	dir := t.TempDir()
	for _, args := range [][]string{
		{"clone", "--quiet", remote, dir},
		{"-C", dir, "config", "user.email", "operator@example.com"},
		{"-C", dir, "config", "user.name", "operator"},
		{"-C", dir, "checkout", "--quiet", "-b", "feature/main"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("other branch"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-C", dir, "add", "README"},
		{"-C", dir, "commit", "--quiet", "-m", "other branch"},
		{"-C", dir, "push", "--quiet", "origin", "feature/main"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	h := newHost(t, remote, "grain-owner-state.secrets.enc")
	h.write(t, "ciphertext-v1")
	out := h.sync(t)
	if !out.Committed || !out.Pushed {
		t.Fatalf("sync against remote with only refs/heads/feature/main = %+v, want Committed and Pushed", out)
	}
	if got := showRemote(t, remote, "grain-owner-state.secrets.enc"); got != "ciphertext-v1" {
		t.Fatalf("remote main branch content = %q, want ciphertext-v1", got)
	}
}

// showRemote reads a file out of the bare remote's branch, which is the
// only thing these tests assert about: what another clone would get.
func showRemote(t *testing.T, remote, file string) string {
	t.Helper()
	return showRemoteOn(t, remote, "main", file)
}

func showRemoteOn(t *testing.T, remote, branch, file string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", remote, "show", branch+":"+file).CombinedOutput()
	if err != nil {
		t.Fatalf("reading %s from %s on the remote: %v: %s", file, branch, err, out)
	}
	return string(out)
}

// seed puts one commit on the bare remote's default branch, so that a
// clone of it is not the empty-repository case.
func seed(t *testing.T, remote, file, content string) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	run("clone", "--quiet", remote, dir)
	run("config", "user.email", "operator@example.com")
	run("config", "user.name", "operator")
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "--", file)
	run("commit", "--quiet", "-m", "seed")
	run("push", "--quiet", "origin", "main")
}
