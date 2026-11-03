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

package model

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	// GitCapabilityServerID is the canonical capability_id of the git
	// server. Its git-repo method with a credential argument is answered
	// by grain itself (pkg/capability/githubtoken.Method): the run's git
	// traffic for the repository named goes through grain's git proxy
	// with the named credential (formerly github-credential:<name>).
	GitCapabilityServerID = "https://grain.dev/capabilities/git"
	GitRepoMethod         = "git-repo"
	GitRepoRef            = GitCapabilityServerID + "#" + GitRepoMethod

	// GitCredentialAnyRepo is git-repo's repo value for every repository
	// the task's run reaches, on every host: the scope a retired
	// github-credential:<name> grant had, and what schema migration 103
	// rewrote each one to.
	GitCredentialAnyRepo = "*"

	// GitHubCapabilityServerID is the canonical capability_id of the
	// in-process github server, and GitHubSandboxRepoRef its sandbox-repo
	// method: a private, single-use GitHub repository for the run, with a
	// token scoped to it (formerly github-sandbox).
	GitHubCapabilityServerID = "https://grain.dev/capabilities/github"
	GitHubSandboxRepoMethod  = "sandbox-repo"
	GitHubSandboxRepoRef     = GitHubCapabilityServerID + "#" + GitHubSandboxRepoMethod
	GitHubSandboxRepoGrant   = GitHubSandboxRepoRef + "={}"

	// GitHubSandboxCapability is the retired bare name of
	// GitHubSandboxRepoGrant, a one-release alias since schema migration
	// 103.
	GitHubSandboxCapability = "github-sandbox"
)

// gitCredentialNameRE is what a credential name may be: a file name under
// secrets/github, or a git connection's credential name, with nothing a
// JSON string or a policy glob would have to escape.
var gitCredentialNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@-]{0,127}$`)

// GitCredentialNamePattern is git-repo's credential argument's schema
// pattern (gitCredentialNameRE).
const GitCredentialNamePattern = `^[A-Za-z0-9][A-Za-z0-9_.@-]{0,127}$`

// GitCredentialRepoPattern is git-repo's repo argument's schema pattern:
// owner/name, or GitCredentialAnyRepo.
const GitCredentialRepoPattern = `^(?:\*|[A-Za-z0-9][A-Za-z0-9_.-]{0,99}/[A-Za-z0-9][A-Za-z0-9_.-]{0,99})$`

var gitCredentialRepoRE = regexp.MustCompile(GitCredentialRepoPattern)

// ValidGitCredentialName reports whether name can be a git-repo grant's
// credential.
func ValidGitCredentialName(name string) bool { return gitCredentialNameRE.MatchString(name) }

// GitCredentialServerGrant is the recorded git-repo grant routing repo's
// git traffic through the credential named name: the form
// protocol.Normalize records it in (keys sorted).
func GitCredentialServerGrant(name, repo string) string {
	return GitRepoRef + `={"credential":` + strconv.Quote(name) + `,"repo":` + strconv.Quote(repo) + `}`
}

// GitCredentialGrantTarget is the credential a git credential grant names
// and the repository it applies to -- GitCredentialAnyRepo for every one
// -- for the recorded git-repo {credential, repo} grant and for the
// retired bare github-credential:<name>, which applied to every
// repository. false for any other grant, a git-repo grant without a
// credential among them (the external git server's own clone grant).
func GitCredentialGrantTarget(grant string) (name, repo string, ok bool) {
	grant = strings.TrimSpace(grant)
	if n, legacy := GitCredentialName(grant); legacy {
		return n, GitCredentialAnyRepo, true
	}
	if CapabilityGrantRef(grant) != GitRepoRef {
		return "", "", false
	}
	spec, err := CapabilityGrantSpec(grant)
	if err != nil {
		return "", "", false
	}
	name, _ = spec.With["credential"].(string)
	repo, _ = spec.With["repo"].(string)
	if !ValidGitCredentialName(name) || !gitCredentialRepoRE.MatchString(repo) {
		return "", "", false
	}
	return name, repo, true
}

// IsGitCredentialGrant reports whether grant routes git traffic through a
// named credential (GitCredentialGrantTarget).
func IsGitCredentialGrant(grant string) bool {
	_, _, ok := GitCredentialGrantTarget(grant)
	return ok
}

// IsGitHubSandboxGrant reports whether grant is github#sandbox-repo,
// recorded, bare reference, or the retired github-sandbox.
func IsGitHubSandboxGrant(grant string) bool {
	grant = strings.TrimSpace(grant)
	return grant == GitHubSandboxCapability || CapabilityGrantRef(grant) == GitHubSandboxRepoRef
}

// gitCredentialOverride returns the credential name a git credential
// grant among grants asks for owner/repo, if any: a grant naming that
// repository wins over one naming every repository (GitCredentialAnyRepo),
// and an empty owner or repo -- a task with no target -- is answered only
// by the latter.
//
// A task holding more than one grant of the same reach gets the first in
// the order of the grant strings -- which, coming from
// Store.GitCredentialOverride, is the capability id sorted ascending, so
// the same task resolves to the same token on every request rather than
// to whichever row was read first. Picking a winner rather than refusing
// is deliberate: two named tokens on one task is an odd thing to ask for,
// not a dangerous one (both are credentials this deployment already
// trusts a task with), and failing the run over it would be a worse
// answer than quietly using one of them.
//
// A retired github-credential:<name> and the git-repo {credential: <name>,
// repo: "*"} grant migration 103 rewrote it to resolve the same, and two
// of either order by credential name alike, so a task whose rows were
// rewritten resolves to the token it resolved to before.
func gitCredentialOverride(grants []Grant, owner, repo string) (name string, ok bool) {
	type candidate struct{ name, key string }
	var exact, any []candidate
	want := owner + "/" + repo
	for _, g := range grants {
		n, r, isOverride := GitCredentialGrantTarget(g.Capability)
		if !isOverride {
			continue
		}
		c := candidate{name: n, key: n + "\x00" + g.Capability}
		switch {
		case r == GitCredentialAnyRepo:
			any = append(any, c)
		case owner != "" && repo != "" && strings.EqualFold(r, want):
			exact = append(exact, c)
		}
	}
	for _, set := range [][]candidate{exact, any} {
		if len(set) == 0 {
			continue
		}
		sort.Slice(set, func(i, j int) bool { return set[i].key < set[j].key })
		return set[0].name, true
	}
	return "", false
}
