// Package grainci is the `grain-ci` capability: the grant a task holds
// when grain's own CI (pkg/ci) judges the branches it pushes.
//
// It is a GRANT provider in the shape selfdebug already established --
// it mints nothing, places nothing and writes no prompt section, so
// every method but Spec is model.BaseCapability's default. What it
// grants is not material in a sandbox: it is that grain will run this
// repository's `.grain/workflows` for the branch a task pushes, and --
// since grain/task-302 -- that the verdict holds the merge queue, a red
// run keeping the pull request out of it exactly as a red GitHub check
// does (orchestrator.withGrainCI). It also registers the read-only CI
// tools (list_ci_runs, get_ci_log), which is why grain's own review and
// self-debug templates carry it with model.GrantByGrain.
//
// # Nobody attaches it by hand
//
// It was once the switch itself, settable per task, per repo and
// deployment-wide. Since 57939261f the switch is the repository's CI
// Mode ("Use Grain CI", model.RepoUsesGrainCI), and this grant follows
// from it: model.adjustGrainCIGrant and ui.Client.creationGrants give
// grain-ci to every task on a repository grain CI judges, and strip it
// from a task on any other. One switch per repository, so a repository
// cannot be half gated -- one task held by grain CI while its siblings
// merge on GitHub's word.
//
// So it is not in ui.OfferedCapabilities, and every place a person
// names capabilities -- the task picker, `grain create -capability`, a
// repo's default set, the deployment's -- refuses it with Refusal,
// which names the CI Mode instead of "unknown capability". A grant
// that could be ticked and then silently did nothing is what this
// replaced (task 2015).
//
// # What it does not grant
//
// Any credential, and any privilege inside the sandbox the jobs run in.
// A workflow file is a file every agent grain dispatches can rewrite,
// so it must never be able to widen what anything may do -- see
// pkg/ci's own "What a workflow file is trusted with", which is the
// argument this capability exists to keep true: this decides *whether*
// a repository's workflows run, and nothing about what they may reach
// when they do.
package grainci

import (
	"fmt"

	"github.com/ai-on-gke/grain/pkg/model"
)

// CapabilityName is the grant id a task's model.Grant names it by.
const CapabilityName = "grain-ci"

// Description is the one sentence this grant is described by in the
// registry (Spec) and anywhere a held grant is listed.
const Description = "Run this repository's own .grain/workflows in grain's sandboxes for the branches this task pushes, and hold the merge queue until they pass"

// Provider is the grain-ci capability. Spec is the whole of it -- see
// the package doc on why there is nothing to mint, place or revoke.
type Provider struct {
	model.BaseCapability
}

// New returns a grain-ci Provider, ready to register.
func New() *Provider { return &Provider{} }

// Spec implements model.CapabilityProvider.
func (p *Provider) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{
		Name:        CapabilityName,
		Label:       "grain CI",
		Description: Description,
		Source:      model.GrantByLabel,
		Provision:   model.ProvisionGrant,
	}
}

// RepoUsesGrainCI reports whether the repository is configured to use Grain CI.
// It checks whether repo is the controller-local config repo, or if repo
// configuration enables Grain CI (via CIMode or IgnoreGitHubCI).
func RepoUsesGrainCI(repoCfg *model.RepoConfig, repo model.RepoRef) bool {
	return model.RepoUsesGrainCI(repoCfg, repo)
}

// Refusal is what grain answers anybody who names grain-ci in a set of
// capabilities they are choosing -- a task's, a repo's default set or
// the deployment's. It names the switch that does what they were
// reaching for, so the next move is on the repo's page rather than in
// a support question.
func Refusal() string {
	return fmt.Sprintf("%s cannot be attached by hand: grain grants it to every task on a repository whose CI Mode is %q "+
		"(the repo's page, Settings -> Repos), and to no task on any other. Set the CI Mode instead",
		CapabilityName, "Use Grain CI")
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

// Granted reports whether a task holds this capability -- the per-task
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
		if g.Capability == CapabilityName {
			return true
		}
	}
	return false
}
