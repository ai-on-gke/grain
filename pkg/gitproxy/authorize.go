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

// What replaces the static repo-allowlist.json from grain/proxy: a
// sandbox may touch exactly the repos its own currently-live run is
// scoped to -- the target it was dispatched against for read and write,
// its task's Reads for read only -- answered by asking model's store
// rather than a file an operator keeps in sync by hand. A sandbox with
// no live run authorizes nothing, which is the same fail-closed default
// a missing allowlist file gave, arrived at without one existing.

import (
	"context"
	"fmt"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Authorizer decides whether a sandbox may perform action against
// owner/repo through the proxy. For a repository on a git host other
// than the default, owner is host/owner (GitRequest.ScopeOwner, the
// form model.RepoRef.StoredOwner writes), so an implementation that
// compares owners as strings keeps one host's repositories apart from
// another's without knowing about hosts at all.
type Authorizer interface {
	Authorize(ctx context.Context, sandbox, owner, repo, action string) (bool, error)
}

// Refuser is an Authorizer that refuses some repos to every sandbox and
// can say why. Optional: Handle asks for it and carries on without it,
// so a test stub or a future Authorizer that has no such repos need not
// implement anything. ModelAuthorizer does, for the one repo that has to
// be refused this way -- grain's own state, when it holds the encrypted
// secrets file.
type Refuser interface {
	Refusal(owner, repo string) (reason string, refused bool)
}

// ScopeLookup resolves a sandbox to the write target its live run was
// dispatched against, the repos it may fetch but never push, and the repos
// it may push to. model.Store implements this today via GitScope; a test
// supplies a stub instead.
type ScopeLookup interface {
	GitScope(ctx context.Context, sandbox string) (*model.RepoRef, []model.RepoRef, []model.RepoRef, error)
}

// CredentialOverrideLookup resolves a sandbox to the named credential its
// live task asks the proxy to use in place of the owner/repo ladder --
// bwsalmon/agents#52's `grain-github-<name>` label, by way of
// model.GitCredentialGrant. model.Store implements this via
// GitCredentialOverride; a test supplies a stub instead. Kept separate
// from ScopeLookup because it answers a different question (which
// credential, not which repo) that GitProxy.Handle only needs to ask
// once authorization has already allowed the request through.
type CredentialOverrideLookup interface {
	GitCredentialOverride(ctx context.Context, sandbox, owner, repo string) (name string, ok bool, err error)
}

// HostCredentialOverrideLookup is CredentialOverrideLookup for a
// repository on a git host, empty meaning the default host
// (model.Store.GitCredentialOverrideOn). Optional: a lookup without it
// answers the default host only, and a request for any other host is
// refused rather than resolved as if it were on the default one.
type HostCredentialOverrideLookup interface {
	GitCredentialOverrideOn(ctx context.Context, sandbox, host, owner, repo string) (name string, ok bool, err error)
}

// HostTeamCredentialLookup is TeamCredentialLookup for a repository on a
// git host, empty meaning the default host (model.Store.TeamGitCredentialOn),
// optional the way HostCredentialOverrideLookup is.
type HostTeamCredentialLookup interface {
	TeamGitCredentialOn(ctx context.Context, team, host, owner, repo string) (string, error)
}

// TeamCredentialLookup resolves a team to the named credential it clones
// and pushes owner/repo with, or an error wrapping
// model.ErrNoTeamGitCredential when it has none. model.Store implements
// this via TeamGitCredential. It answers for the CI sandboxes
// registered in GitProxy.SandboxTeams, which have no task behind them
// for CredentialOverrideLookup to resolve.
type TeamCredentialLookup interface {
	TeamGitCredential(ctx context.Context, team, owner, repo string) (string, error)
}

// ModelAuthorizer is the Authorizer backed by the live task model.
type ModelAuthorizer struct {
	Store ScopeLookup
	// Forbidden names repos no sandbox may reach through this proxy, in
	// any way, whatever its task scope says -- checked before the scope
	// is even read, so being a task's own Target does not get past it.
	//
	// It exists for one repository: grain's own state (pkg/staterepo)
	// when that repository holds, or has ever held, the encrypted
	// secrets file. Dispatching a task at the state repository is a
	// supported and intended thing to do -- it is how a settings change
	// becomes a pull request -- but authorization here is per repository
	// and cannot be anything else: this proxy streams a packfile it does
	// not parse, so a sandbox allowed to fetch a repository is a sandbox
	// that gets every object in it, including a file deleted long ago.
	// A state repository written by a build that kept secrets.enc in it
	// is therefore one no sandbox may clone, and saying so here is the
	// only place that can be enforced. cmd/grain's startGitProxy decides
	// which repo, if any, that is; see staterepo.Repo.HasSecrets.
	//
	// A holder rather than a slice because which repository that is can
	// change under a running proxy: adopting a state repository points
	// the installation at a different one without a restart, so the
	// answer is re-read into this set rather than baked in at startup
	// (ForbiddenSet's own doc comment). Nil forbids nothing.
	Forbidden *ForbiddenSet

	// Branches resolves the one branch a sandbox may push
	// (AuthorizeRefUpdates). Nil reads it from Store when Store can
	// answer, as model.Store does; with neither, every push is refused.
	Branches BranchLookup
}

// ForbiddenReason is the message a request for a Forbidden repo is
// refused with. It is returned to the caller, so it says what to do
// rather than only that the answer is no: an agent whose task really
// does target that repository would otherwise read a bare denial as
// grain being broken.
const ForbiddenReason = "grain's own state repository holds (or once held) its encrypted secrets file, " +
	"so no sandbox may fetch or push it: everything a sandbox can clone is everything it can read. " +
	"A state repository that has never held that file can be dispatched against normally"

// forbids reports whether owner/repo is one of the repos no sandbox may
// reach at all, as the set stands at this moment rather than as it stood
// when this authorizer was built.
func (a *ModelAuthorizer) forbids(owner, repo string) bool {
	return a.Forbidden.forbids(owner, repo)
}

// Refusal implements Refuser: it is how the denial above reaches the
// caller as an explanation rather than as the generic "not in scope"
// every other refusal gets.
func (a *ModelAuthorizer) Refusal(owner, repo string) (string, bool) {
	if a.forbids(canonicalizeRepo(owner, repo)) {
		return ForbiddenReason, true
	}
	return "", false
}

// Authorize reports whether sandbox may perform action against owner/repo.
// git-receive-pack (push) is allowed against the run's own write target
// or any of its task's granted write targets (Writes) -- and then, in
// every one of them, only to its own branch (AuthorizeRefUpdates, which
// Handle asks once the push's command list is in hand). A push's info/refs
// advertisement (?service=git-receive-pack) arrives here as
// "git-receive-pack" too: HandleRequest authorizes it as the push it
// opens (isPush). Every other action
// (info/refs, git-upload-pack) is allowed against that target or any Reads
// or Writes entry.
func (a *ModelAuthorizer) Authorize(ctx context.Context, sandbox, owner, repo, action string) (bool, error) {
	// Before the scope is read at all: a forbidden repo is forbidden to
	// every sandbox, and a request for one must not depend on a store
	// read that could fail open if this were ordered the other way.
	if a.forbids(canonicalizeRepo(owner, repo)) {
		return false, nil
	}
	target, reads, writes, err := a.Store.GitScope(ctx, sandbox)
	if err != nil {
		return false, fmt.Errorf("gitproxy: resolving scope for %s: %w", sandbox, err)
	}
	owner, repo = canonicalizeRepo(owner, repo)

	matchesTarget := target != nil && canonicalMatches(*target, owner, repo)
	if action == "git-receive-pack" {
		if matchesTarget {
			return true, nil
		}
		for _, w := range writes {
			if canonicalMatches(w, owner, repo) {
				return true, nil
			}
		}
		return false, nil
	}
	if matchesTarget {
		return true, nil
	}
	for _, r := range reads {
		if canonicalMatches(r, owner, repo) {
			return true, nil
		}
	}
	for _, w := range writes {
		if canonicalMatches(w, owner, repo) {
			return true, nil
		}
	}
	return false, nil
}

// canonicalMatches reports whether r is the canonicalized owner/repo,
// owner carrying r's host when it is off the default host
// (GitRequest.ScopeOwner).
func canonicalMatches(r model.RepoRef, owner, repo string) bool {
	rOwner, rRepo := canonicalizeRepo(r.StoredOwner(), r.Name)
	return rOwner == owner && rRepo == repo
}
