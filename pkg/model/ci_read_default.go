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
	"database/sql"
	"errors"
	"slices"
)

// The default CI read grant (task 4364).
//
// A task targeting a repository grain CI gates is filed holding grain#ci by
// default (adjustGrainCIGrant), which among other things lets it read that
// repository's CI: `grain ci list/show/log`, `grain pr status`, `ci://`. A
// task targeting a repository GitHub Actions gates (ci_mode: github) has
// grain#ci stripped, because grain#ci there would change merge gating
// (grainci.GrantedExplicitly) -- and before this it was left with no CI read
// at all, so every `grain ci log` it tried answered "forbidden" and agents
// asked a person for grain#ci, a grant nobody can give on such a repository.
//
// So a task on a GitHub-CI repository is filed holding
// api:ci-read@<target> by default instead: read-only, limited to the task's
// own repository, and opening nothing but its CI runs, jobs and logs (the
// ci:read scope, docs/agent-api.md). It mints no credential and changes no
// gate. A deployment turns it off with a capability policy deny rule on
// https://grain.dev/capabilities/api#ci-read -- for every repository, or
// for some with the rule's repos -- the same rule that already refuses the
// grant to anybody who asks for it.

// RepoCIDefaults is the CI grant a task filed against one repository is
// given by default: grain#ci when grain CI gates it, and api:ci-read@<repo>
// when it does not and the capability policy permits that default.
type RepoCIDefaults struct {
	// GrainCI is RepoUsesGrainCI: the task is given grain#ci by default.
	GrainCI bool
	// CIRead is whether the task is given api:ci-read@<repo> by default
	// (DefaultCIReadGrant). Never true alongside GrainCI.
	CIRead bool
}

// DefaultCIReadGrant is the grant a task targeting repo, a repository grain
// CI does not gate, is filed holding by default: api:ci-read limited to
// repo, credited as a default.
func DefaultCIReadGrant(repo RepoRef) Grant {
	return Grant{Capability: APIGrantString("ci-read", repo.String()), Via: GrantByDefault}
}

// IsDefaultCIReadGrant reports whether g is DefaultCIReadGrant(repo) as it is
// recorded on a task: a default api:ci-read reaching repo's CI and nothing
// beyond it.
func IsDefaultCIReadGrant(g Grant, repo RepoRef) bool {
	if g.Via != GrantByDefault || repo == (RepoRef{}) {
		return false
	}
	return CanonicalGrantKey(g.Capability) == CanonicalGrantKey(DefaultCIReadGrant(repo).Capability)
}

// CIReadDefaultPermitted reports whether policy lets a task targeting repo
// hold the default api:ci-read@<repo>. A deny rule on
// https://grain.dev/capabilities/api#ci-read (optionally narrowed with
// repos) is the opt-out; an allow rule naming it that does not admit repo
// refuses it as well, exactly as it would refuse it to a person asking.
func CIReadDefaultPermitted(policy CapabilityPolicy, repo RepoRef) bool {
	if repo == (RepoRef{}) {
		return false
	}
	r := repo
	return policy.Permits(DefaultCIReadGrant(repo).Capability, CapabilitySubject{Repo: &r, Kind: CapabilityRunTask}) == nil
}

// RepoCIDefaultsFor is RepoCIDefaults for a task targeting repo, given
// whether grain CI gates it and the deployment's capability policy.
func RepoCIDefaultsFor(usesGrainCI bool, policy CapabilityPolicy, repo RepoRef) RepoCIDefaults {
	if usesGrainCI {
		return RepoCIDefaults{GrainCI: true}
	}
	return RepoCIDefaults{CIRead: CIReadDefaultPermitted(policy, repo)}
}

// RepoCIDefaults reads RepoCIDefaults for a task targeting repo.
func (s *Store) RepoCIDefaults(ctx context.Context, repo RepoRef) (RepoCIDefaults, error) {
	return querierRepoCIDefaults(ctx, s.db, repo)
}

func querierRepoCIDefaults(ctx context.Context, q querier, repo RepoRef) (RepoCIDefaults, error) {
	usesGrainCI, err := querierRepoUsesGrainCI(ctx, q, repo)
	if err != nil || usesGrainCI {
		return RepoCIDefaults{GrainCI: usesGrainCI}, err
	}
	policy, err := querierCapabilityPolicy(ctx, q)
	if err != nil {
		return RepoCIDefaults{}, err
	}
	return RepoCIDefaultsFor(false, policy, repo), nil
}

// querierCapabilityPolicy reads Config.CapabilityPolicy alone, the zero
// policy (which permits everything) on a deployment with no config row.
func querierCapabilityPolicy(ctx context.Context, q querier) (CapabilityPolicy, error) {
	var text sql.NullString
	err := q.QueryRowContext(ctx, "SELECT `capability_policy` FROM `grain_config` WHERE `id` = 1").Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return CapabilityPolicy{}, nil
	}
	if err != nil {
		return CapabilityPolicy{}, err
	}
	return DecodeCapabilityPolicy(text.String)
}

// AdjustRepoCIGrants is adjustGrainCIGrant plus the default CI read: it adds
// grain#ci to grants when d.GrainCI and strips it otherwise, then adds
// DefaultCIReadGrant(repo) when d.CIRead and nothing in grants already
// reads repo's CI. Like adjustGrainCIGrant it never appends into grants'
// spare capacity.
func AdjustRepoCIGrants(grants []Grant, repo RepoRef, d RepoCIDefaults) []Grant {
	grants = adjustGrainCIGrant(grants, d.GrainCI)
	if !d.CIRead || d.GrainCI || grantsReadCI(grants, repo) {
		return grants
	}
	return append(slices.Clip(grants), DefaultCIReadGrant(repo))
}

// grantsReadCI reports whether grants already open ci:read on repo.
func grantsReadCI(grants []Grant, repo RepoRef) bool {
	names := make([]string, len(grants))
	for i, g := range grants {
		names[i] = g.Capability
	}
	return APIGrantsOf(names).Allows("ci:read", repo.String())
}

// applyRepoCIGrants is applyGrainCIGrant plus the default CI read: on a
// repository grain CI does not gate, after grain#ci is stripped (and
// replaced by api:ci-read@<target> where a declaration asked for it), a
// composition that still cannot read its target's CI is given
// DefaultCIReadGrant, credited to LayerRepo.
func applyRepoCIGrants(comp *Composition, d RepoCIDefaults) {
	applyGrainCIGrant(comp, d.GrainCI)
	if d.GrainCI || !d.CIRead || comp.Target == nil || *comp.Target == (RepoRef{}) {
		return
	}
	if grantsReadCI(comp.Grants, *comp.Target) {
		return
	}
	sources := make([]CompositionLayer, len(comp.Grants))
	for i := range comp.Grants {
		if i < len(comp.GrantSources) && comp.GrantSources[i] != "" {
			sources[i] = comp.GrantSources[i]
		} else {
			sources[i] = comp.Sources.Grants
		}
	}
	hadGrants := len(comp.Grants) > 0
	comp.Grants = append(slices.Clip(comp.Grants), DefaultCIReadGrant(*comp.Target))
	comp.GrantSources = append(sources, LayerRepo)
	if !hadGrants {
		comp.Sources.Grants = LayerRepo
	}
}

// GrantsBeyondCIReadDefault is t's grants minus the default api:ci-read on
// its own target (IsDefaultCIReadGrant): what a proposal asks its proposer's
// permission to cover. That default, like a default grain#ci
// (escalatedCapabilities), is not something the proposal asked for -- every
// task filed against the repository carries it -- and it reads nothing
// beyond the repository the proposal is filed against, whose landing
// LandsWhereGranted judges on its own.
func GrantsBeyondCIReadDefault(t Task) []Grant {
	if t.Target == nil || !slices.ContainsFunc(t.Grants, func(g Grant) bool { return IsDefaultCIReadGrant(g, *t.Target) }) {
		return t.Grants
	}
	return slices.DeleteFunc(slices.Clone(t.Grants), func(g Grant) bool { return IsDefaultCIReadGrant(g, *t.Target) })
}
