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

package github_test

// The one claim orchestrator.refreshStaleHead rests on that this
// repository cannot settle by itself: that GitHub's
// POST /repos/{owner}/{repo}/merges really answers the three ways
// RESTClient.MergeBranch reads it -- 201 to MergeResult{Merged: true}
// with a sha, 204 to MergeResult{} with a *nil error*, and 409 to an
// *github.Error that IsMergeConflict says yes to.
//
// Every other test of that mapping is against a stand-in: github_test.go
// drives it off canned ApiResponses, and githubsim answers all three by
// running real `git` against a real bare repository, which makes the
// merges real but leaves the HTTP githubsim's own idea of it.
// docs/merge-queue-staleness.md has asked since it was written for the
// mapping to meet a live repository once, because refreshStaleHead reads
// its whole classification of "was this head simply behind" off these
// codes: a 204 misread as "merged" burns a pull request's one refresh
// without refreshing anything, and a 409 misread as an ordinary error
// turns a genuine conflict into a cycle that errors forever.
//
// So this file is the check, written to be run rather than described.
// It needs a credential with `contents: write` on any repository at all
// -- the endpoint under test does not care whose, and nothing here
// touches bwsalmon/grain -- which a sandbox does not have by design
// (pkg/capability/githubtoken: "the token never enters the sandbox").
// The github-sandbox capability supplies exactly that, and under it the
// whole check is one command with nothing to fill in:
//
//	GRAIN_LIVE_GITHUB_MERGES=1 go test ./pkg/github -run Live -v
//
// The sim-backed test at the bottom runs the identical body against
// githubsim on every ordinary `go test`. It is not there to re-cover
// what githubsim_test.go already covers; it is there so that the live
// harness above is known to still work on the day somebody finally has
// a credential to point it at.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
)

// The two files pkg/capability/githubsandbox's Materialize places in a
// sandbox granted that capability: the repo it minted, as owner/repo,
// and a token scoped to only it. Spelled out here rather than imported
// so that pkg/github's tests take no dependency on pkg/capability, which
// depends on this package.
const (
	sandboxTokenPath = "/home/debian/.github-sandbox-token"
	sandboxRepoPath  = "/home/debian/.github-sandbox-repo"
)

// mergesFixture is one repository this check can be run against: a git
// remote it may force-push three branches to, and an API client
// addressing that same repository. The live and the simulated runs
// differ in nothing else, which is the point -- the assertions below are
// then literally the same assertions.
type mergesFixture struct {
	owner, repo   string
	defaultBranch string
	// gitURL is what `git push` is given: an https URL carrying the
	// token for the live run, a local path to the bare repo for the
	// simulated one.
	gitURL string
	client *github.RESTClient
}

// The live run. Skipped unless GRAIN_LIVE_GITHUB_MERGES is set, the same
// opt-in shape real-VM tests use -- this one writes to a
// real repository over the network, so "run wherever it happens to work"
// is the wrong default even where a credential is lying around.
//
// The repository and token are read from the github-sandbox placements
// if they are there, so a run granted that capability needs to name
// nothing; GRAIN_LIVE_GITHUB_REPO and GRAIN_LIVE_GITHUB_TOKEN override
// both, for a fine-grained token on a throwaway repository instead.
func TestMergeBranchStatusMappingAgainstLiveGitHub(t *testing.T) {
	if os.Getenv("GRAIN_LIVE_GITHUB_MERGES") == "" {
		t.Skip("GRAIN_LIVE_GITHUB_MERGES not set; this one writes to a real repository over the network (set it to 1, with a contents:write credential in place, to run it)")
	}
	requireGit(t)

	repo := strings.TrimSpace(os.Getenv("GRAIN_LIVE_GITHUB_REPO"))
	if repo == "" {
		repo = strings.TrimSpace(readIfPresent(t, sandboxRepoPath))
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		t.Fatalf("no repository to test against: set GRAIN_LIVE_GITHUB_REPO to owner/repo, or grant this run the github-sandbox capability, which writes one to %s (got %q)", sandboxRepoPath, repo)
	}

	token := strings.TrimSpace(os.Getenv("GRAIN_LIVE_GITHUB_TOKEN"))
	if token == "" {
		token = strings.TrimSpace(readIfPresent(t, sandboxTokenPath))
	}
	if token == "" {
		t.Fatalf("no credential to test with: set GRAIN_LIVE_GITHUB_TOKEN to a token carrying contents:write on %s, or grant this run the github-sandbox capability, which writes one to %s", repo, sandboxTokenPath)
	}

	client := github.NewClient(github.NewRealTransport("api.github.com"), github.StaticToken{Token: &token})
	defaultBranch, err := client.DefaultBranch(owner, name)
	if err != nil || defaultBranch == "" {
		// An empty repository still reports a default branch, so this
		// failing means the credential cannot see the repository at all
		// -- worth saying before the push says it less clearly.
		t.Fatalf("reading %s's default branch: %v (branch %q)", repo, err, defaultBranch)
	}

	runMergeBranchStatusMapping(t, mergesFixture{
		owner:         owner,
		repo:          name,
		defaultBranch: defaultBranch,
		// x-access-token is the username half GitHub documents for an App
		// installation token, and is accepted for a fine-grained token too.
		gitURL: fmt.Sprintf("https://x-access-token:%s@github.com/%s/%s.git", token, owner, name),
		client: client,
	})
}

// The same body against githubsim, on every ordinary `go test` -- see
// this file's own header for why it is here.
func TestMergeBranchStatusMappingAgainstTheSim(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	bare := filepath.Join(dir, "repo.git")
	runGitForMerges(t, dir, "git", "init", "--bare", "-q", "-b", "main", bare)
	// Both keys, for the same reason pkg/cli/bare_remote_test.go's
	// initBareRepo sets them: a push that triggers background
	// maintenance should be what waits for it, not a later assertion.
	runGitForMerges(t, bare, "git", "config", "maintenance.autoDetach", "false")
	runGitForMerges(t, bare, "git", "config", "gc.autoDetach", "false")

	sim := githubsim.New("acme", "widgets", bare, "main")
	runMergeBranchStatusMapping(t, mergesFixture{
		owner:         "acme",
		repo:          "widgets",
		defaultBranch: "main",
		gitURL:        bare,
		client:        github.NewClient(sim, nil),
	})
}

// runMergeBranchStatusMapping is the check itself: three MergeBranch
// calls in refreshStaleHead's own direction -- base is the pull
// request's head branch and head is the base branch, i.e. a merge of the
// default branch *into* the branch -- and, after each, a read of the
// branch to confirm the endpoint's answer describes what it really did.
func runMergeBranchStatusMapping(t *testing.T, f mergesFixture) {
	t.Helper()
	behind, conflicting := seedMergesFixture(t, f)

	message := "Merge " + f.defaultBranch + " into " + behind

	// 201: the branch was strictly behind, so the merge lands and names
	// the commit it wrote.
	merged, err := f.client.MergeBranch(f.owner, f.repo, behind, f.defaultBranch, message)
	if err != nil {
		t.Fatalf("merging %q into a branch strictly behind it: %v", f.defaultBranch, err)
	}
	if !merged.Merged || merged.SHA == "" {
		t.Fatalf("got %+v, want a merge that landed and named its commit", merged)
	}
	head, err := f.client.GetBranchHead(f.owner, f.repo, behind)
	if err != nil || head == nil {
		t.Fatalf("reading %q back: head=%+v err=%v", behind, head, err)
	}
	if head.SHA != merged.SHA {
		t.Fatalf("%q is at %q, but the endpoint reported writing %q -- a 201 that did not move the branch would have refreshStaleHead report a refresh that never happened", behind, head.SHA, merged.SHA)
	}

	// 204: the very same call again. The branch now contains the default
	// branch, nothing is written, and -- the half refreshStaleHead is
	// most exposed to -- this is an answer, not an error.
	again, err := f.client.MergeBranch(f.owner, f.repo, behind, f.defaultBranch, message)
	if err != nil {
		t.Fatalf("an already-up-to-date branch has to come back as a nil error, or refreshStaleHead's default arm turns it into a cycle-failing error: %v", err)
	}
	if again.Merged || again.SHA != "" {
		t.Fatalf("got %+v, want the zero MergeResult -- refreshStaleHead reads Merged as \"the branch moved\" and would spend the pull request's one refresh on nothing", again)
	}
	after, err := f.client.GetBranchHead(f.owner, f.repo, behind)
	if err != nil || after == nil || after.SHA != head.SHA {
		t.Fatalf("branch head = %+v err=%v, want it left at %q by a merge that wrote nothing", after, err, head.SHA)
	}

	// 409: a branch carrying its own edit to a line the default branch
	// also moved. The one outcome an automatic merge cannot fix, so it
	// has to be distinguishable from every transient failure.
	before, err := f.client.GetBranchHead(f.owner, f.repo, conflicting)
	if err != nil || before == nil {
		t.Fatalf("reading %q: head=%+v err=%v", conflicting, before, err)
	}
	_, err = f.client.MergeBranch(f.owner, f.repo, conflicting, f.defaultBranch,
		"Merge "+f.defaultBranch+" into "+conflicting)
	if !github.IsMergeConflict(err) {
		t.Fatalf("got %v, want a conflict IsMergeConflict recognises -- anything else and refreshStaleHead retries a merge that can never succeed", err)
	}
	var ghErr *github.Error
	if !errors.As(err, &ghErr) || ghErr.Status != 409 {
		t.Fatalf("got %v, want an *github.Error carrying 409", err)
	}
	stillThere, err := f.client.GetBranchHead(f.owner, f.repo, conflicting)
	if err != nil || stillThere == nil || stillThere.SHA != before.SHA {
		t.Fatalf("branch head = %+v err=%v, want it untouched at %q by a merge that conflicted", stillThere, err, before.SHA)
	}
}

// seedMergesFixture force-pushes the shape the three calls need onto
// f.gitURL, and returns the two branch names it made:
//
//	A ── B         f.defaultBranch, B edits SHARED.md's one line
//	└─ behind      at A: strictly behind, and nothing to arbitrate
//	└─ conflicting at A + its own edit to that same line
//
// Force, and a fresh local repository rather than a clone, so that the
// starting state is this function's and not whatever the repository
// happened to already hold -- a github-sandbox repo is created empty,
// but a throwaway somebody points GRAIN_LIVE_GITHUB_REPO at is not.
func seedMergesFixture(t *testing.T, f mergesFixture) (behind, conflicting string) {
	t.Helper()
	wd := filepath.Join(t.TempDir(), "work")
	runGitForMerges(t, filepath.Dir(wd), "git", "init", "-q", "-b", f.defaultBranch, wd)
	runGitForMerges(t, wd, "git", "config", "user.email", "merges-check@example.com")
	runGitForMerges(t, wd, "git", "config", "user.name", "grain merges check")
	runGitForMerges(t, wd, "git", "remote", "add", "origin", f.gitURL)

	writeSharedLine(t, wd, "one line, as both branches were cut from it")
	runGitForMerges(t, wd, "git", "add", "SHARED.md")
	runGitForMerges(t, wd, "git", "commit", "-q", "-m", "the commit both branches are cut from")

	behind, conflicting = "grain-merges-check/behind", "grain-merges-check/conflicting"
	runGitForMerges(t, wd, "git", "push", "-q", "--force", "origin",
		"HEAD:refs/heads/"+f.defaultBranch, "HEAD:refs/heads/"+behind, "HEAD:refs/heads/"+conflicting)

	runGitForMerges(t, wd, "git", "checkout", "-q", "-B", conflicting)
	writeSharedLine(t, wd, "one line, as the branch rewrote it")
	runGitForMerges(t, wd, "git", "commit", "-q", "-am", "the branch's own edit to the shared line")
	runGitForMerges(t, wd, "git", "push", "-q", "--force", "origin", conflicting)

	runGitForMerges(t, wd, "git", "checkout", "-q", f.defaultBranch)
	writeSharedLine(t, wd, "one line, as "+f.defaultBranch+" rewrote it")
	runGitForMerges(t, wd, "git", "commit", "-q", "-am", "the base branch's own edit to the shared line")
	runGitForMerges(t, wd, "git", "push", "-q", "--force", "origin", f.defaultBranch)

	return behind, conflicting
}

func writeSharedLine(t *testing.T, wd, line string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(wd, "SHARED.md"), []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func readIfPresent(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// runGitForMerges folds a git command's own output into the failure, and
// keeps a token that reached the command line out of it -- the live run
// puts one in origin's URL, and git names the remote in most of what it
// prints. Named for this file because this codebase duplicates a small
// test helper across packages rather than sharing one
// (tests/e2e/harness_test.go's own comment on why).
func runGitForMerges(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	// Nothing here should ever prompt: a bad credential has to fail the
	// test rather than block it.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, redactCredentials(string(out)))
	}
}

// redactCredentials blanks the password half of any https URL in s, so a
// failure this test prints cannot carry the token that was in origin's
// URL into a CI log.
func redactCredentials(s string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "https://"); i >= 0 {
			rest := line[i+len("https://"):]
			if at := strings.Index(rest, "@"); at >= 0 {
				if colon := strings.Index(rest[:at], ":"); colon >= 0 {
					line = line[:i+len("https://")] + rest[:colon+1] + "<redacted>" + rest[at:]
				}
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
