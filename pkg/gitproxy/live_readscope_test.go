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

package gitproxy_test

// The CI half of live_test.go, against the same rig: a real bare repo
// served by `git http-backend`, behind a real GitProxy over a real
// embedded SQLite store, driven by a real `git` process.
//
// What is different here is what is *not* in the model. A grain CI job's
// sandbox has no task and no run behind it (pkg/ci), so the store has
// nothing to say about it and ModelAuthorizer refuses it everything. The
// only thing that lets its checkout through is the read scope the CI
// service registered for it, and these tests are what pin what that
// scope is worth: the one repository under test, fetch only, and only
// while the job is running.

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
)

// ciRig is the whole stack one of these tests drives: a stand-in GitHub
// holding two repositories, a proxy in front of it, and the registry the
// CI service grants read scopes into.
type ciRig struct {
	upstreamDir string
	proxyURL    string
	scopes      *gitproxy.ReadScopes
	writeScopes *gitproxy.WriteScopes
}

const ciSandbox = "ci-run1-tests-yml-build"

const ciSandboxToken = "live-test-ci-sandbox-token"

func newCIRig(t *testing.T) ciRig {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	upstreamDir := t.TempDir()
	newBareRepoWithOneCommit(t, upstreamDir, "owner", "widgets")
	newBareRepoWithOneCommit(t, upstreamDir, "owner", "secrets-of-another-repo")
	backend := httptest.NewServer(gitHTTPBackend(upstreamDir))
	t.Cleanup(backend.Close)

	// A store with no task and no run in it at all: nothing here
	// authorizes the CI sandbox below, which is the point.
	store, _ := openStore(t)

	dataDir := t.TempDir()
	mustWriteFile(t, filepath.Join(dataDir, "secrets", "github", "credentials.json"), `{"*": "anonymous"}`)
	mustWriteFile(t, filepath.Join(dataDir, "secrets", "sandbox-tokens.json"),
		fmt.Sprintf(`{%q: %q}`, ciSandbox, ciSandboxToken))

	scopes := gitproxy.NewReadScopes()
	writeScopes := gitproxy.NewWriteScopes()
	proxy, err := gitproxy.BuildProxy(gitproxy.BuildConfig{
		DataDir: dataDir, Store: store,
		ForwardHost: mustHost(t, backend.URL), ForwardTLS: false,
		ReadScopes:  scopes,
		WriteScopes: writeScopes,
	})
	if err != nil {
		t.Fatal(err)
	}
	// What the daemon does at startup: the deployment's git connection, which is all the proxy reads.
	if err := store.UpdateConfig(context.Background(), func(t *model.Config) error {
		t.GitConnections = append(t.GitConnections, model.TeamGitConnection{Credential: "anonymous"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	proxyServer := httptest.NewServer(gitproxy.NewHandler(proxy))
	t.Cleanup(proxyServer.Close)

	return ciRig{upstreamDir: upstreamDir, proxyURL: proxyServer.URL, scopes: scopes, writeScopes: writeScopes}
}

// ciGit runs one git command as the CI sandbox would: with the sandbox's
// own bearer token, which is the only identity it has.
func ciGit(t *testing.T, dir string, args ...string) ([]byte, error) {
	t.Helper()
	full := append([]string{
		"-c", "http.extraHeader=Authorization: Basic " + basicAuth(ciSandboxToken),
	}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

func (r ciRig) remote(repo string) string { return r.proxyURL + "/" + repo + ".git" }

// TestLiveACISandboxMayFetchTheRepoUnderTest is the checkout a job cannot
// start without, through a proxy whose model knows nothing about this
// sandbox.
func TestLiveACISandboxMayFetchTheRepoUnderTest(t *testing.T) {
	rig := newCIRig(t)
	rig.scopes.Grant(ciSandbox, model.RepoRef{Owner: "owner", Name: "widgets"})

	dir := t.TempDir()
	if out, err := ciGit(t, dir, "clone", "--quiet", rig.remote("owner/widgets"), filepath.Join(dir, "work")); err != nil {
		t.Fatalf("a CI sandbox should be able to clone the repo under test: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "work", "README.md")); err != nil {
		t.Fatalf("the clone left no checkout: %v", err)
	}
}

// TestLiveACISandboxMayNotPushTheRepoUnderTest is the property the whole
// scope exists for: a workflow file is a file every agent grain
// dispatches can rewrite, so the sandbox that runs one must not be able
// to write anything back.
func TestLiveACISandboxMayNotPushTheRepoUnderTest(t *testing.T) {
	rig := newCIRig(t)
	rig.scopes.Grant(ciSandbox, model.RepoRef{Owner: "owner", Name: "widgets"})

	dir := t.TempDir()
	clone := filepath.Join(dir, "work")
	if out, err := ciGit(t, dir, "clone", "--quiet", rig.remote("owner/widgets"), clone); err != nil {
		t.Fatalf("cloning the repo under test: %v\n%s", err, out)
	}
	for _, kv := range [][2]string{{"user.email", "ci@example.com"}, {"user.name", "ci"}} {
		run(t, clone, "git", "config", kv[0], kv[1])
	}
	if err := os.WriteFile(filepath.Join(clone, "README.md"), []byte("a workflow was here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, clone, "git", "add", "README.md")
	run(t, clone, "git", "commit", "-q", "-m", "a workflow rewrote the repository")

	out, err := ciGit(t, clone, "push", "origin", "HEAD:main")
	if err == nil {
		t.Fatalf("a CI sandbox pushed the repo under test; it must not be able to:\n%s", out)
	}

	// And the upstream is untouched -- the refusal happened before the
	// pack reached the stand-in GitHub, not after.
	tip := runOutput(t, rig.upstreamDir, "git", "--git-dir",
		filepath.Join(rig.upstreamDir, "owner", "widgets.git"), "log", "-1", "--format=%s", "main")
	if strings.TrimSpace(tip) != "initial commit" {
		t.Errorf("upstream main tip = %q, want the seed commit -- the push landed", strings.TrimSpace(tip))
	}
}

// TestLiveACISandboxMayNotFetchASecondRepository: the grant is one
// repository, not "read access".
func TestLiveACISandboxMayNotFetchASecondRepository(t *testing.T) {
	rig := newCIRig(t)
	rig.scopes.Grant(ciSandbox, model.RepoRef{Owner: "owner", Name: "widgets"})

	dir := t.TempDir()
	out, err := ciGit(t, dir, "clone", "--quiet", rig.remote("owner/secrets-of-another-repo"), filepath.Join(dir, "other"))
	if err == nil {
		t.Fatalf("a CI sandbox cloned a repository its job is not testing:\n%s", out)
	}
}

// TestLiveACISandboxMayFetchMultipleGrantedRepositories: when granted multiple
// repositories, the CI sandbox can fetch any of them.
func TestLiveACISandboxMayFetchMultipleGrantedRepositories(t *testing.T) {
	rig := newCIRig(t)
	rig.scopes.Grant(ciSandbox, model.RepoRef{Owner: "owner", Name: "widgets"})
	rig.scopes.Grant(ciSandbox, model.RepoRef{Owner: "owner", Name: "secrets-of-another-repo"})

	dir := t.TempDir()
	if out, err := ciGit(t, dir, "clone", "--quiet", rig.remote("owner/widgets"), filepath.Join(dir, "work")); err != nil {
		t.Fatalf("cloning first granted repository: %v\n%s", err, out)
	}
	if out, err := ciGit(t, dir, "clone", "--quiet", rig.remote("owner/secrets-of-another-repo"), filepath.Join(dir, "other")); err != nil {
		t.Fatalf("cloning second granted repository: %v\n%s", err, out)
	}
}

// TestLiveACISandboxScopeIsGoneOnceTheJobIsReleased: the sandbox's token
// outlives nothing. Dropping the grant is what the CI service does as it
// releases the sandbox, and from that moment the very fetch that worked
// a line ago is refused.
func TestLiveACISandboxScopeIsGoneOnceTheJobIsReleased(t *testing.T) {
	rig := newCIRig(t)
	rig.scopes.Grant(ciSandbox, model.RepoRef{Owner: "owner", Name: "widgets"})

	dir := t.TempDir()
	if out, err := ciGit(t, dir, "clone", "--quiet", rig.remote("owner/widgets"), filepath.Join(dir, "during")); err != nil {
		t.Fatalf("cloning while the job is running: %v\n%s", err, out)
	}

	rig.scopes.Drop(ciSandbox)
	if n := rig.scopes.Len(); n != 0 {
		t.Fatalf("read scopes still holds %d grant(s) after the job was released", n)
	}
	out, err := ciGit(t, dir, "clone", "--quiet", rig.remote("owner/widgets"), filepath.Join(dir, "after"))
	if err == nil {
		t.Fatalf("the repo under test is still fetchable after the job was released:\n%s", out)
	}
}

// TestLiveACISandboxWithWriteScopeMayPushGrantedRepository: a CI sandbox holding
// a write scope may push commits back to the granted repository.
func TestLiveACISandboxWithWriteScopeMayPushGrantedRepository(t *testing.T) {
	rig := newCIRig(t)
	rig.writeScopes.Grant(ciSandbox, model.RepoRef{Owner: "owner", Name: "widgets"})

	dir := t.TempDir()
	clone := filepath.Join(dir, "work")
	if out, err := ciGit(t, dir, "clone", "--quiet", rig.remote("owner/widgets"), clone); err != nil {
		t.Fatalf("cloning the repo with write scope: %v\n%s", err, out)
	}
	for _, kv := range [][2]string{{"user.email", "ci@example.com"}, {"user.name", "ci"}} {
		run(t, clone, "git", "config", kv[0], kv[1])
	}
	if err := os.WriteFile(filepath.Join(clone, "RELEASE.md"), []byte("v1.0.0 released\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, clone, "git", "add", "RELEASE.md")
	run(t, clone, "git", "commit", "-q", "-m", "release commit")

	if out, err := ciGit(t, clone, "push", "origin", "HEAD:main"); err == nil {
		t.Fatalf("a CI sandbox with write scope should not be able to update main:\n%s", out)
	}
	if out, err := ciGit(t, clone, "push", "origin", "HEAD:refs/heads/candidate"); err != nil {
		t.Fatalf("a CI sandbox with write scope should be able to push candidate:\n%v\n%s", err, out)
	}

	// Verify the push reached upstream.
	tip := runOutput(t, rig.upstreamDir, "git", "--git-dir",
		filepath.Join(rig.upstreamDir, "owner", "widgets.git"), "log", "-1", "--format=%s", "candidate")
	if strings.TrimSpace(tip) != "release commit" {
		t.Errorf("upstream candidate tip = %q, want 'release commit'", strings.TrimSpace(tip))
	}
}

// TestLiveACISandboxWithWriteScopeMayNotPushUngrantedRepository: write scope to one
// repository does not grant write or read access to another.
func TestLiveACISandboxWithWriteScopeMayNotPushUngrantedRepository(t *testing.T) {
	rig := newCIRig(t)
	rig.writeScopes.Grant(ciSandbox, model.RepoRef{Owner: "owner", Name: "widgets"})

	dir := t.TempDir()
	out, err := ciGit(t, dir, "clone", "--quiet", rig.remote("owner/secrets-of-another-repo"), filepath.Join(dir, "other"))
	if err == nil {
		t.Fatalf("a CI sandbox cloned an ungranted repository:\n%s", out)
	}
}
