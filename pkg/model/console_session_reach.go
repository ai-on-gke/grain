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
	"context"
	"slices"
	"strings"
)

// What every standing session may read without anybody granting it
// repository by repository (grain/task-2641).
//
// A session exists to answer "what is going on in this deployment", and
// the answer is spread over every repository the deployment works in and
// the CI that judges them. A session that could read only the state
// repository had to be handed each of the others by hand, and one that
// was not -- the releases session on 2026-11-02, asked whether a pull
// request it could not clone had merged -- simply could not answer. So
// two things are the default for every session, the shipped director and
// any an operator creates alike:
//
//   - Read access to every repository this deployment works in
//     (ConsoleSessionWorkingRepos), resolved again on every cycle, so a
//     repository added to repo_config later is readable without editing
//     the session. ConsoleSession.ReadsAll opts a session out.
//   - Read access to CI for every repository (ConsoleSessionCIReadGrant),
//     granted GrantByDefault. ConsoleSession.DeclinedDefaults, or
//     removing it from the session's task, opts a session out, and the
//     opt-out sticks (orchestrator.mergeConsoleSessionGrants).
//
// Neither is a write: a session files tasks rather than pushing, and
// what it may push to stays ConsoleSession.Writes, named one repository
// at a time.

// ConsoleSessionCIReadGrant is the CI-read grant every console session
// holds by default: the agent API's ci-read preset with no repository, so
// `grain ci list/show/log` and `grain pr status --task/--ref` reach every
// repository's CI, grain CI's and GitHub Actions' alike
// (Store.SandboxNamespaceScope's MayReadAllCI). Not grain-ci, which is a
// repository's CI Mode -- whether grain runs its workflows and holds the
// merge queue on them -- rather than a read, and which nobody may attach
// by hand.
const ConsoleSessionCIReadGrant = APICapabilityServerID + "#ci-read"

// consoleSessionDefaultGrants are the capabilities every session's task
// is granted GrantByDefault unless the session declines them. Unlike the
// director's own set (DefaultConsoleSessions), these reach a session
// whatever its stored Capabilities say -- including one saved before the
// grant was a default -- because they are not written into that list.
var consoleSessionDefaultGrants = []string{ConsoleSessionCIReadGrant}

// ConsoleSessionDefaultGrants returns the capabilities every console
// session holds by default, granted GrantByDefault.
func ConsoleSessionDefaultGrants() []string { return slices.Clone(consoleSessionDefaultGrants) }

// IsConsoleSessionDefaultGrant reports whether id is one of
// ConsoleSessionDefaultGrants, in either its canonical .../api# or legacy
// api: spelling.
func IsConsoleSessionDefaultGrant(id string) bool {
	key := CanonicalGrantKey(id)
	return slices.ContainsFunc(consoleSessionDefaultGrants, func(d string) bool {
		return CanonicalGrantKey(d) == key
	})
}

// ReadsAllRepos reports whether the session reads every repository this
// deployment works in: true unless ReadsAll says false.
func (s ConsoleSession) ReadsAllRepos() bool { return s.ReadsAll == nil || *s.ReadsAll }

// DeclinesDefault reports whether the session has opted out of default
// grant id (DeclinedDefaults), matching across canonical and legacy spellings.
func (s ConsoleSession) DeclinesDefault(id string) bool {
	key := CanonicalGrantKey(id)
	return slices.ContainsFunc(s.DeclinedDefaults, func(d string) bool {
		return CanonicalGrantKey(d) == key
	})
}

// consoleSessionDefaultGrantsFor is the default grants session is given
// that its own Capabilities do not already name.
func consoleSessionDefaultGrantsFor(session ConsoleSession) []string {
	var out []string
	for _, id := range consoleSessionDefaultGrants {
		key := CanonicalGrantKey(id)
		if session.DeclinesDefault(id) || slices.ContainsFunc(session.Capabilities, func(c string) bool {
			return CanonicalGrantKey(c) == key
		}) {
			continue
		}
		out = append(out, id)
	}
	return out
}

// ConsoleSessionWorkingRepos is every repository this deployment works
// in, as a console session reads them: each repo_config row, plus each
// repository Config.TargetRepos names outright (not by pattern), less
// any this deployment has retired -- a repository whose work ownership
// is off, which is another deployment's now (or nobody's) and whose
// clone is no part of what is going on here -- and any TargetRepos does
// not allow. Sorted, so a task's reads do not churn between cycles.
func ConsoleSessionWorkingRepos(ctx context.Context, store *Store) ([]RepoRef, error) {
	if store == nil {
		return nil, nil
	}
	cfg, err := store.GetConfig(ctx)
	if err != nil {
		return nil, err
	}
	var targets []string
	if cfg != nil {
		targets = cfg.TargetRepos
	}
	rows, err := store.ListRepoConfigs(ctx)
	if err != nil {
		return nil, err
	}
	var out []RepoRef
	add := func(r RepoRef) {
		if r.Owner == "" || r.Name == "" || !RepoAllowed(targets, r) {
			return
		}
		if slices.ContainsFunc(out, func(e RepoRef) bool { return e.Same(r) }) {
			return
		}
		out = append(out, r)
	}
	retired := func(r RepoRef) bool {
		return slices.ContainsFunc(rows, func(c RepoConfig) bool {
			return c.Repo.Same(r) && !c.Work.RunsByDefault()
		})
	}
	for _, c := range rows {
		if !retired(c.Repo) {
			add(c.Repo)
		}
	}
	for _, t := range targets {
		t = strings.TrimSpace(t)
		if t == "" || strings.ContainsAny(t, "*?[") {
			continue
		}
		r, err := ParseRepo(t)
		if err != nil || retired(r) {
			continue
		}
		add(r)
	}
	slices.SortFunc(out, func(a, b RepoRef) int { return strings.Compare(a.String(), b.String()) })
	return out, nil
}

// ConsoleSessionAutoReads is the repositories session reads because it
// reads every working repository (ReadsAllRepos), less the state
// repository and those it names itself, which it reads either way: none
// for a session that opted out.
func ConsoleSessionAutoReads(ctx context.Context, store *Store, session ConsoleSession, stateRepo RepoRef) ([]RepoRef, error) {
	if !session.ReadsAllRepos() {
		return []RepoRef{}, nil
	}
	working, err := ConsoleSessionWorkingRepos(ctx, store)
	if err != nil {
		return nil, err
	}
	out := []RepoRef{}
	for _, r := range working {
		if (stateRepo.Owner != "" && r.Same(stateRepo)) ||
			slices.ContainsFunc(session.Reads, func(s RepoRef) bool { return s.Same(r) }) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}
