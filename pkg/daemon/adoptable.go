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

import (
	"context"
	"slices"

	"github.com/ai-on-gke/grain/pkg/model"
)

// adoptableFrameworks is the frameworks whose runs this daemon can carry
// across its own restart: the ones whose CLI outlives the daemon process,
// so the next daemon's adoption pass (orchestrator.RecoverRunHosts, then
// ResumeAdoptedRun) finds it still running. That is claude under a run
// host (-run-host-image, Config.RunHost) and any CLI started through
// grain hostd (ResolveCLIRun's ViaHostd). Every other run is a child of
// the daemon, which a restart drains and finishes as interrupted -- a
// durable task's run then comes back as a restart suspension instead
// (docs/design/durable-tasks.md#DT-1, "Adoption across restarts and
// upgrades").
func (c Config) adoptableFrameworks(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	if c.RunHost != nil {
		out[model.AgentFrameworkClaude] = true
	}
	for _, fw := range model.CLIRunFrameworks {
		if c.ResolveCLIRun(ctx, fw).Via == ViaHostd {
			out[model.NormalizeAgentFrameworkName(fw)] = true
		}
	}
	return out
}

// preferAdoptable is a durable request's placement: its default models
// on an adoptable framework (adoptableFrameworks) are tried before the
// rest, each group in the deployment's own order, and candidatesLocked
// keeps only adoptable connections when any of them can take the run.
// It is weaker than a pin -- a pinned model or connection is left as it
// is -- and stronger than the rotation, whose order it overrides.
func (r *poolRequest) preferAdoptable(adoptable map[string]bool) {
	if r.pinned != nil || r.pinnedModel || len(adoptable) == 0 {
		return
	}
	r.adoptable = adoptable
	slices.SortStableFunc(r.models, func(a, b string) int {
		ia, ib := r.isAdoptable(a), r.isAdoptable(b)
		switch {
		case ia && !ib:
			return -1
		case ib && !ia:
			return 1
		}
		return 0
	})
	if len(r.models) > 0 {
		r.model = r.models[0]
	}
}

// isAdoptable reports whether a run of m on r lands on an adoptable
// framework.
func (r poolRequest) isAdoptable(m string) bool {
	return r.adoptable[model.NormalizeAgentFrameworkName(r.frameworkFor(m))]
}

// adoptableOnly is candidates narrowed to the connections on an adoptable
// framework, or candidates whole when r prefers none or none of them is.
func (r poolRequest) adoptableOnly(candidates []model.AgentBackend) []model.AgentBackend {
	if len(r.adoptable) == 0 {
		return candidates
	}
	var kept []model.AgentBackend
	for _, b := range candidates {
		if r.adoptable[model.NormalizeAgentFrameworkName(b.Framework)] {
			kept = append(kept, b)
		}
	}
	if len(kept) == 0 {
		return candidates
	}
	return kept
}

// preferPrevious reorders r.models for an unpinned interactive or
// resumable-session task so the model and framework of its previous
// attempt are tried before the rest of the default rotation, preserving
// warm CLI sessions (--resume) across runs unless that credential/model
// is paused or unavailable.
func (r *poolRequest) preferPrevious(framework, modelName string) {
	if r.pinned != nil || r.pinnedModel {
		return
	}
	r.preferFramework = model.NormalizeAgentFrameworkName(framework)
	r.preferModel = modelName
	if r.preferFramework == "" && r.preferModel == "" {
		return
	}
	rank := func(m string) int {
		mf := model.NormalizeAgentFrameworkName(r.frameworkFor(m))
		if r.preferModel != "" && m == r.preferModel && (r.preferFramework == "" || mf == r.preferFramework) {
			return 0
		}
		if r.preferFramework != "" && mf == r.preferFramework {
			return 1
		}
		if r.preferModel != "" && m == r.preferModel {
			return 2
		}
		return 3
	}
	slices.SortStableFunc(r.models, func(a, b string) int {
		return rank(a) - rank(b)
	})
	if len(r.models) > 0 {
		r.model = r.models[0]
	}
}

// preferredOnly narrows candidates to connections on r.preferFramework
// (and supporting r.preferModel when r.model is unset) when any candidate
// matches, falling back to all candidates otherwise.
func (r poolRequest) preferredOnly(candidates []model.AgentBackend) []model.AgentBackend {
	if r.preferFramework == "" && r.preferModel == "" {
		return candidates
	}
	if r.preferModel != "" && r.model == "" {
		var exact []model.AgentBackend
		for _, b := range candidates {
			if (r.preferFramework == "" || model.NormalizeAgentFrameworkName(b.Framework) == r.preferFramework) && b.SupportsModel(r.preferModel) {
				exact = append(exact, b)
			}
		}
		if len(exact) > 0 {
			return exact
		}
	}
	if r.preferFramework == "" {
		return candidates
	}
	var kept []model.AgentBackend
	for _, b := range candidates {
		if model.NormalizeAgentFrameworkName(b.Framework) == r.preferFramework {
			kept = append(kept, b)
		}
	}
	if len(kept) == 0 {
		return candidates
	}
	return kept
}
