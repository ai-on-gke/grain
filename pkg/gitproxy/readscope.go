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

// A scope that belongs to a sandbox rather than to a task.
//
// Every scope this proxy has ever answered with came from the live task
// model: a sandbox is resolved to the run on it, the run to its task,
// and the task's own Target and Reads are what it may touch
// (ModelAuthorizer). That is the right answer for the only kind of
// sandbox grain used to build, because every one of them was built for a
// task.
//
// A grain CI job's sandbox is not (pkg/ci). It is built for one job of
// one run against one commit, there is no task behind it, and so
// GitScope resolves it to nothing and every fetch it makes is refused --
// including the checkout the job cannot start without. What it needs is
// a scope of its own, and a much smaller one than any task's: this
// sandbox, this one repository, fetch only.
//
// So the CI service registers the pair while a job is running and drops
// it when the job ends, and ReadOnlyScopeAuthorizer consults that
// registry before falling through to the model. Three properties are
// what make it safe to have a second source of scope at all, and each is
// a line of code below rather than a convention a caller has to keep:
//
//   - it can only ever grant a *fetch*. git-receive-pack is refused here
//     whatever is registered, so a workflow file -- a file every agent
//     grain dispatches can rewrite -- cannot push anywhere with it.
//   - it grants exactly one repository per sandbox, the one the job is
//     testing. Registering a second replaces the first rather than
//     adding to it.
//   - it grants nothing at all once the job is released, because the
//     registration is gone. A CI sandbox's token outliving its job (or
//     being stolen from the file) authorizes as little as a finished
//     run's does.
//
// What it deliberately does *not* override is the forbidden set: a
// repository no sandbox may reach is one no CI job may reach either, so
// ReadOnlyScopeAuthorizer checks it before it consults its own registry
// (see ModelAuthorizer.Forbidden for what is in it and why).

import (
	"context"
	"sync"

	"github.com/ai-on-gke/grain/pkg/model"
)

// ReadScopes is the registry: which sandbox may fetch which
// repositories, as it stands right now.
//
// Every method is safe on a nil *ReadScopes, which reads as an empty
// registry -- a proxy wired up without one grants nothing this way, and
// a CI service that was handed none grants nothing either. Both are
// fail-closed: the grant is missing, so the fetch is refused, rather
// than the fetch being allowed by something that was never asked.
type ReadScopes struct {
	mu    sync.RWMutex
	repos map[string]map[model.RepoRef]struct{}
}

// NewReadScopes returns an empty registry, ready for a CI service to
// grant into and a proxy to read from at the same time.
func NewReadScopes() *ReadScopes {
	return &ReadScopes{repos: make(map[string]map[model.RepoRef]struct{})}
}

// Grant records that sandbox may fetch repo, and nothing else. Called as
// a CI job's sandbox is built, before anything in it runs git.
//
// Granting multiple times for one sandbox adds to its allowed set.
func (s *ReadScopes) Grant(sandbox string, repo model.RepoRef) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.repos == nil {
		s.repos = make(map[string]map[model.RepoRef]struct{})
	}
	set := s.repos[sandbox]
	if set == nil {
		set = make(map[model.RepoRef]struct{})
		s.repos[sandbox] = set
	}
	set[repo] = struct{}{}
}

// Drop forgets all grants for sandbox. Called as the job's sandbox is released,
// success or failure alike, and safe to call for a sandbox that has none
// -- a job that failed before it was granted anything has nothing to
// drop, and dropping twice is what a retried cleanup does.
func (s *ReadScopes) Drop(sandbox string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.repos, sandbox)
}

// Repo returns one of the repositories sandbox may fetch, and false
// where it may fetch none.
func (s *ReadScopes) Repo(sandbox string) (model.RepoRef, bool) {
	if s == nil {
		return model.RepoRef{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	set := s.repos[sandbox]
	for repo := range set {
		return repo, true
	}
	return model.RepoRef{}, false
}

// Repos returns all repositories sandbox may fetch.
func (s *ReadScopes) Repos(sandbox string) []model.RepoRef {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	set := s.repos[sandbox]
	if len(set) == 0 {
		return nil
	}
	out := make([]model.RepoRef, 0, len(set))
	for repo := range set {
		out = append(out, repo)
	}
	return out
}

// Len is how many sandboxes hold a grant right now -- for a test, and
// for anything that wants to say how many CI jobs are in flight.
func (s *ReadScopes) Len() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.repos)
}

// allows reports whether this registry, on its own, permits action
// against owner/repo from sandbox.
//
// A push is refused before the registry is even read: there is no
// registration that could allow one, and saying so here rather than in
// the caller is what makes "read-only" a property of the type rather
// than of whoever wires it.
func (s *ReadScopes) allows(sandbox, owner, repo, action string) bool {
	if action == "git-receive-pack" {
		return false
	}
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	set := s.repos[sandbox]
	if len(set) == 0 {
		return false
	}
	cOwner, cRepo := canonicalizeRepo(owner, repo)
	for granted := range set {
		if canonicalMatches(granted, cOwner, cRepo) {
			return true
		}
	}
	return false
}

// WriteScopes is the registry of write scopes: which sandbox may push to
// (and fetch from) which repositories in CI.
//
// Every method is safe on a nil *WriteScopes, which reads as an empty registry.
type WriteScopes struct {
	mu    sync.RWMutex
	repos map[string]map[model.RepoRef]struct{}
}

// NewWriteScopes returns an empty write scope registry.
func NewWriteScopes() *WriteScopes {
	return &WriteScopes{repos: make(map[string]map[model.RepoRef]struct{})}
}

// Grant records that sandbox may push to (and fetch from) repo.
func (s *WriteScopes) Grant(sandbox string, repo model.RepoRef) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.repos == nil {
		s.repos = make(map[string]map[model.RepoRef]struct{})
	}
	set := s.repos[sandbox]
	if set == nil {
		set = make(map[model.RepoRef]struct{})
		s.repos[sandbox] = set
	}
	set[repo] = struct{}{}
}

// Drop forgets all write grants for sandbox.
func (s *WriteScopes) Drop(sandbox string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.repos, sandbox)
}

// Repos returns all repositories sandbox has write access to.
func (s *WriteScopes) Repos(sandbox string) []model.RepoRef {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	set := s.repos[sandbox]
	if len(set) == 0 {
		return nil
	}
	out := make([]model.RepoRef, 0, len(set))
	for repo := range set {
		out = append(out, repo)
	}
	return out
}

// Len is how many sandboxes hold a write grant right now.
func (s *WriteScopes) Len() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.repos)
}

// allows reports whether this registry permits action against owner/repo from sandbox.
// WriteScopes allows all git actions (fetch and push: git-upload-pack, info/refs, git-receive-pack).
func (s *WriteScopes) allows(sandbox, owner, repo, action string) bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	set := s.repos[sandbox]
	if len(set) == 0 {
		return false
	}
	cOwner, cRepo := canonicalizeRepo(owner, repo)
	for granted := range set {
		if canonicalMatches(granted, cOwner, cRepo) {
			return true
		}
	}
	return false
}

// ReadOnlyScopeAuthorizer answers from a ReadScopes registry first and
// falls through to Next -- the model-backed authorizer, in a deployment
// -- for every sandbox that holds no read grant.
//
// That order is deliberate and costs nothing: a CI sandbox has no live
// run behind it, so asking the model about it is a store read that can
// only ever answer "nothing". The ordinary case (an agent run's sandbox)
// reaches Next after one map lookup.
type ReadOnlyScopeAuthorizer struct {
	// Scopes is the read-only registry. nil grants nothing.
	Scopes *ReadScopes
	// WriteScopes is the write registry. nil grants nothing.
	WriteScopes *WriteScopes
	// Next is consulted for everything Scopes does not allow. Required.
	Next Authorizer
	// Forbidden is the set of repos no sandbox may reach at all, checked
	// before Scopes -- so a read grant cannot be the thing that gets a
	// CI job into the one repository this proxy refuses to everybody
	// (ModelAuthorizer.Forbidden). GitProxy.Handle already refuses those
	// through Refusal below, before Authorize is ever called; this is
	// the same answer given again where the decision is actually made,
	// so that a caller wiring this by hand cannot end up with a scope
	// that outranks the refusal.
	Forbidden *ForbiddenSet
}

// Authorize implements Authorizer.
func (a *ReadOnlyScopeAuthorizer) Authorize(ctx context.Context, sandbox, owner, repo, action string) (bool, error) {
	if a.Forbidden.forbids(canonicalizeRepo(owner, repo)) {
		return false, nil
	}
	if a.WriteScopes.allows(sandbox, owner, repo, action) {
		return true, nil
	}
	if a.Scopes.allows(sandbox, owner, repo, action) {
		return true, nil
	}
	return a.Next.Authorize(ctx, sandbox, owner, repo, action)
}

// Refusal implements Refuser by passing the question straight on to Next
// where Next answers it at all. A wrapper that swallowed it would turn
// the one refusal this proxy explains into the generic "not in scope",
// which is exactly the confusion ForbiddenReason exists to prevent.
func (a *ReadOnlyScopeAuthorizer) Refusal(owner, repo string) (string, bool) {
	if refuser, ok := a.Next.(Refuser); ok {
		return refuser.Refusal(owner, repo)
	}
	return "", false
}

// AuthorizeRefUpdates implements RefAuthorizer. It has to: Handle only
// checks a push's refs when its Authorizer is a RefAuthorizer, and this
// wrapper is the Authorizer every deployment runs (BuildProxy puts it in
// front of ModelAuthorizer whenever a scope registry is wired), so a
// wrapper without this method switched the whole check off.
//
// A CI job holding a write grant for owner/repo may update the refs of
// it CIRefUpdates allows: repo-write is that job's own declaration, to a
// workflow whose job is to push (docs/ci-capabilities.md), and that job
// has no task branch to be held to. Every other sandbox is answered by
// Next -- an agent run's, held to its own task branch -- and a Next that
// decides no refs refuses rather than allows.
func (a *ReadOnlyScopeAuthorizer) AuthorizeRefUpdates(ctx context.Context, sandbox, owner, repo string, updates []RefUpdate) (string, error) {
	if a.WriteScopes.allows(sandbox, owner, repo, "git-receive-pack") {
		return CIRefUpdates(pushCheckFrom(ctx), owner, repo, updates), nil
	}
	if refs, ok := a.Next.(RefAuthorizer); ok {
		return refs.AuthorizeRefUpdates(ctx, sandbox, owner, repo, updates)
	}
	return "this proxy cannot tell which refs this sandbox may push, so it refuses every push", nil
}

// SandboxTeams is the registry of which team a CI job's sandbox runs as,
// for the jobs whose repository access is the team's to give
// (grain/task-1654).
//
// A job that declares `repo-read:` or `repo-write:` in its own workflow
// file has said what it needs; the team's GitHub credential is what
// decides whether it gets it. So such a job's sandbox is registered here
// for as long as it runs, and every request it makes through the proxy
// goes out with that team's credential (Store.TeamGitCredential) rather
// than the deployment's own ladder -- GitHub then refuses whatever the
// team's token cannot do, which is how a repository's owner decides
// which teams may reach it. A job that declares neither is not
// registered and is answered exactly as it was before.
//
// Like the scope registries above, every method is safe on a nil
// *SandboxTeams, which registers nothing.
type SandboxTeams struct {
	mu    sync.RWMutex
	teams map[string]string
}

// NewSandboxTeams returns an empty registry.
func NewSandboxTeams() *SandboxTeams {
	return &SandboxTeams{teams: make(map[string]string)}
}

// Grant records that sandbox's git requests go out as team.
func (s *SandboxTeams) Grant(sandbox, team string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.teams == nil {
		s.teams = make(map[string]string)
	}
	s.teams[sandbox] = team
}

// Drop forgets sandbox's team.
func (s *SandboxTeams) Drop(sandbox string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.teams, sandbox)
}

// Team is the team sandbox runs as, and false where none is registered.
func (s *SandboxTeams) Team(sandbox string) (string, bool) {
	if s == nil {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	team, ok := s.teams[sandbox]
	return team, ok
}
