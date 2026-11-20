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
	"encoding/json"
	"slices"
	"strings"

	"github.com/ai-on-gke/grain/pkg/gates"
)

func healthBindingAllowed(b gates.Binding, a GateHealthAccess) bool {
	switch b.Type {
	case "base_health", "nightly_health":
		return a.allows(b.Subject.Repository, false)
	case "publishing_health":
		return a.PublicationRepository != nil && a.PublicationRepository(b.Subject.Repository)
	case "pipeline_health":
		var c gates.PipelineHealthConfig
		if json.Unmarshal(b.Config, &c) != nil {
			return false
		}
		if c.Semantics == "legacy_quiet_v1" && !a.DeploymentWide {
			return false
		}
		for _, repo := range c.Repos {
			if !a.allows(repo, true) {
				return false
			}
		}
		return len(c.Repos) > 0
	default:
		return true
	}
}
func gateFailureBindings(ctx context.Context, q querier, id string, a GateHealthAccess) ([]gates.Binding, error) {
	g, err := gateTaskGoal(ctx, q, id)
	if err != nil {
		return nil, err
	}
	return gateFailureBindingsWithGoal(ctx, q, id, g, a)
}
func gateFailureBindingsWithGoal(ctx context.Context, q querier, id string, g *TaskGoal, a GateHealthAccess) ([]gates.Binding, error) {
	if !healthGoal(g) {
		return gates.Bindings(id), nil
	}
	t, err := getTask(ctx, q, id)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, sql.ErrNoRows
	}
	repos, _, err := healthScopeRepos(ctx, q, *t, *g, a)
	if err != nil {
		return nil, err
	}
	return healthBindings(*t, *g, repos), nil
}
func filterHealthDiagnostics(d *GateObservationDiagnostics, a GateHealthAccess) {
	visible := []GateObservationSample{}
	for _, sample := range d.History {
		allowed := true
		for _, b := range sample.View.Bindings {
			allowed = allowed && healthBindingAllowed(b, a)
		}
		if allowed {
			visible = append(visible, sample)
		}
	}
	if len(visible) == len(d.History) && !a.ScopeLimited {
		return
	}
	// Do not reveal counts or first-seen/evaluation metadata for hidden samples.
	// Visible history is still bounded; counts explicitly cover these samples.
	*d = GateObservationDiagnostics{Counts: map[string]int{}, History: visible}
	for _, sample := range visible {
		for _, family := range strings.Split(sample.Comparison, ";") {
			d.Counts[family]++
		}
	}
}

// An all-repositories legacy steward may observe deployment inventory under an
// explicit broad read ceiling. Inventory never silently becomes the stronger
// profile's expected repository policy.
func healthScopeRepos(ctx context.Context, q querier, t Task, g TaskGoal, a GateHealthAccess) ([]string, bool, error) {
	repos := slices.Clone(g.Spec.Repos)
	slices.Sort(repos)
	repos = slices.Compact(repos)
	if len(repos) > 64 {
		return repos[:64], false, nil
	}
	if g.Kind != GoalPipelineHealthy || len(repos) > 0 {
		return repos, true, nil
	}
	if !a.DeploymentWide {
		return nil, false, nil
	}
	repos, err := targetReposIn(ctx, q)
	if err != nil {
		return nil, false, err
	}
	if len(repos) > 0 && len(repos) <= 64 {
		return repos, true, nil
	}
	if len(repos) > 64 {
		return nil, false, nil
	}
	err = each(ctx, q, "SELECT `owner`,`name` FROM `repo_config` ORDER BY `owner`,`name` LIMIT 65", nil, func(rows *sql.Rows) error {
		var owner, name string
		if e := rows.Scan(&owner, &name); e != nil {
			return e
		}
		repos = append(repos, RepoFromStored(owner, name).String())
		return nil
	})
	if len(repos) > 64 {
		repos = nil
	}
	return repos, false, err
}

func healthRepoScopeOversized(repos []string) bool {
	set := map[string]bool{}
	for _, r := range repos {
		set[r] = true
	}
	return len(set) > 64
}

type gateHealthAccessKey struct{}

// WithGateHealthAccess supplies the ordinary caller's health read ceiling.
// It grants no authority and leaves child comparison selection independent.

func WithGateHealthAccess(ctx context.Context, access GateHealthAccess) context.Context {
	return context.WithValue(ctx, gateHealthAccessKey{}, access)
}
func gateHealthAccessFrom(ctx context.Context) GateHealthAccess {
	access, _ := ctx.Value(gateHealthAccessKey{}).(GateHealthAccess)
	return access
}
