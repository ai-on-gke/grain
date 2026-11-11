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

// Package grainci holds the rules around the grain#ci grant
// (model.GrainCIGrant, https://grain.dev/capabilities/grain#ci={},
// formerly the bare grain-ci in-process capability): the grant a task
// holds when grain's own CI (pkg/ci) judges the branches it pushes.
//
// The grant itself is a method of the in-process grain capability
// server (pkg/capability/grainserver, design/design/unified-grants.design.md §4):
// it mints nothing and places nothing. What it grants is not material
// in a sandbox: it is that grain will run this repository's
// `.grain/workflows` for the branch a task pushes, and -- since
// grain/task-302 -- that the verdict holds the merge queue, a red run
// keeping the pull request out of it exactly as a red GitHub check does
// (orchestrator.withGrainCI). It also registers the read-only CI tools
// (list_ci_runs, get_ci_log), which is why grain's own review and
// self-debug templates carry it with model.GrantByGrain.
//
// # Nobody attaches it by hand
//
// It was once the switch itself, settable per task, per repo and
// deployment-wide. Since 57939261f the switch is the repository's CI
// Mode ("Use Grain CI", model.RepoUsesGrainCI), and this grant follows
// from it: model.adjustGrainCIGrant and ui.Client.creationGrants give
// grain#ci to every task on a repository grain CI judges, and strip it
// from a task on any other. One switch per repository, so a repository
// cannot be half gated -- one task held by grain CI while its siblings
// merge on GitHub's word.
//
// So it is not in ui.OfferedCapabilities, and every place a person
// names capabilities -- the task picker, `grain create -capability`, a
// repo's default set, the deployment's -- refuses it, in either
// spelling, with Refusal, which names the CI Mode instead of "unknown
// capability". A grant that could be ticked and then silently did
// nothing is what this replaced (task 2015).
//
// # What it does not grant
//
// Any credential, and any privilege inside the sandbox the jobs run in.
// A workflow file is a file every agent grain dispatches can rewrite,
// so it must never be able to widen what anything may do -- see
// pkg/ci's own "What a workflow file is trusted with", which is the
// argument this grant exists to keep true: this decides *whether*
// a repository's workflows run, and nothing about what they may reach
// when they do.
package grainci

import (
	"fmt"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Grant is the grant grain records on a task its repository's CI Mode
// puts through grain CI.
const Grant = model.GrainCIGrant

// LegacyName is the retired bare name of Grant, still read as it
// (model.IsGrainCIGrant) wherever nothing rewrote it.
const LegacyName = model.GrainCICapability

// Description is what grain says the grant does, wherever it is listed.
const Description = "Run this repository's own .grain/workflows in grain's sandboxes for the branches this task pushes, and hold the merge queue until they pass"

// IsGrant reports whether id names grain#ci, recorded or bare, or the
// retired grain-ci alias.
func IsGrant(id string) bool { return model.IsGrainCIGrant(id) }

// RepoUsesGrainCI reports whether the repository is configured to use Grain CI.
// It checks whether repo is the controller-local config repo, or if repo
// configuration enables Grain CI (via CIMode or IgnoreGitHubCI).
func RepoUsesGrainCI(repoCfg *model.RepoConfig, repo model.RepoRef) bool {
	return model.RepoUsesGrainCI(repoCfg, repo)
}

// Refusal is what grain answers anybody who names grain#ci (or the
// retired grain-ci) in a set of capabilities they are choosing -- a
// task's, a repo's default set, the deployment's, or a run's
// request_access. It names what does what they were reaching for, which
// depends on the repository the grant would be for:
//
//   - mode CIModeGrain: the repository's tasks hold grain#ci already, so
//     there is nothing to ask for.
//   - any other mode, with repo known: the repository deliberately does
//     not use grain CI, so the advice is not to switch it but to read its
//     CI with api:ci-read@<repo> (SubstituteGrant) -- what grain#ci asked
//     for in a declaration is rewritten to (model.applyGrainCIGrant) --
//     and that `grain ci wait` and `grain pr open` need no grant at all.
//   - repo unknown ("" -- a deployment's default set): both, and the CI
//     Mode for whoever wants grain CI to gate a repository.
//
// It names the switch or the grant, so the next move is on the repo's
// page or in a grant rather than in a support question.
func Refusal(mode model.CIMode, repo string) string {
	head := fmt.Sprintf("%s (formerly %s) cannot be attached by hand: grain grants it to every task on a repository whose CI Mode is %q "+
		"(the repo's page, Settings -> Repos), and to no task on any other",
		model.GrainCIRef, LegacyName, "Use Grain CI")
	repo = strings.TrimSpace(repo)
	switch {
	case mode == model.CIModeGrain && repo != "":
		return fmt.Sprintf("%s. %s uses grain CI, so its tasks hold it already: there is nothing to ask for", head, repo)
	case repo != "":
		return fmt.Sprintf("%s. %s's CI Mode is %q: `grain ci wait` and `grain pr open` need no grant there, and to read its CI runs and logs "+
			"(grain ci list/show/log, grain pr status, ci://) grant %s with {repo: %q} instead (%s)",
			head, repo, ciModeLabel(mode), model.APIRef(substituteMethod), repo, SubstituteGrant(repo))
	}
	return fmt.Sprintf("%s. To read a repository's CI without grain CI gating it, grant %s scoped to that repository (api:ci-read@<owner/name>); "+
		"to have grain CI gate a repository, set its CI Mode instead", head, model.APIRef(substituteMethod))
}

// substituteMethod is the api server method SubstituteGrant names.
const substituteMethod = "ci-read"

// SubstituteGrant is what stands in for grain#ci on a repository grain CI
// does not gate: the read-only, repo-scoped api:ci-read@<repo>, the same
// grant model.applyGrainCIGrant substitutes for a declaration's grain#ci.
func SubstituteGrant(repo string) string { return model.APIGrantString(substituteMethod, repo) }

// SubstituteRequest is SubstituteGrant as a request_access item asks for
// it: the api server's ci-read method with the repository as its argument.
func SubstituteRequest(repo string) (capability string, with map[string]any) {
	return model.APIRef(substituteMethod), map[string]any{"repo": repo}
}

// ModeOf is the CI Mode Refusal is given for repo: the configured one,
// CIModeGrain wherever model.RepoUsesGrainCI says grain CI gates repo.
func ModeOf(repoCfg *model.RepoConfig, repo model.RepoRef) model.CIMode {
	if RepoUsesGrainCI(repoCfg, repo) {
		return model.CIModeGrain
	}
	if repoCfg != nil && repoCfg.CIMode != "" {
		return repoCfg.CIMode
	}
	return model.CIModeGitHub
}

func ciModeLabel(mode model.CIMode) string {
	switch mode {
	case model.CIModeNone:
		return "Use No CI"
	case model.CIModeGrain:
		return "Use Grain CI"
	}
	return "Use GitHub CI"
}

// RequiredFor reports whether repo is one Grain must check without an
// operator first opting it in. The controller-local config repository is
// executable deployment configuration: merging a dump Grain cannot import
// can stop the deployment on its next start, so its validation is part of
// the repository's own contract rather than an optional per-repo setting.
//
// Ordinary repositories remain opt-in through their CI Mode. Keeping this one
// exception here gives task creation, the CI API and merge admission the same
// answer instead of teaching each of them separately that grain/config is
// special.
func RequiredFor(repo model.RepoRef) bool { return model.IsConfigRepo(repo) }

// Granted reports whether a task holds grain#ci -- the per-task
// half of the same question, and what decides that a finished run's
// branch is put through CI at all (orchestrator.reconcileTaskCI).
//
// A task's grants are where the repository's CI Mode has already landed
// by the time it exists: ui.Client.creationGrants and
// model.adjustGrainCIGrant add or strip this grant from the target
// repository's mode when the task is filed, so reading the task alone
// is reading that mode.
func Granted(grants []model.Grant) bool {
	for _, g := range grants {
		if model.IsGrainCIGrant(g.Capability) {
			return true
		}
	}
	return false
}

// GrantedExplicitly reports whether grants includes a grain#ci grant that did
// not come from the repository's or deployment's default (model.GrantByDefault).
func GrantedExplicitly(grants []model.Grant) bool {
	for _, g := range grants {
		if model.IsGrainCIGrant(g.Capability) && g.Via != model.GrantByDefault {
			return true
		}
	}
	return false
}
