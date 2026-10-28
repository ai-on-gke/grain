// Package releasenotes is a GRANT model.CapabilityProvider: a task
// holding it may read how much work this deployment did over a window
// (model.ClusterStats) and publish a week's release notes
// (model.ReleaseNote), through the two tools its own MCP server exposes
// -- read_cluster_stats and write_release_notes.
//
// It mints nothing and writes no Placement -- Resolve, Materialize,
// PromptSection and Revoke are all model.BaseCapability's defaults --
// because what it grants is not material moved into a sandbox, it is two
// tools, exactly as pkg/capability/memory grants three.
//
// A capability rather than something every run can do, for memory's
// reason turned outward. What a run writes here is not a note to the
// next agent; it is this deployment's public account of itself, exported
// to the state repository as `release-notes/<week>.md` and shown in the
// UI as what the cluster has been doing. A run that could rewrite that
// could rewrite the record of a week it had no part in, and there would
// be nothing in the file to say so. So it is a grant a human attaches
// deliberately -- to the weekly job grain seeds for it
// (model.ReleaseNotesTemplateID), and to whatever else somebody decides
// should be able to publish on this deployment's behalf.
//
// Reading the numbers is on the same grant as writing the prose, rather
// than open to every run, and that is the arrangement that makes the
// notes worth anything: the figures a week's notes are built on are one
// set of aggregates over grain's own record, computed by the daemon, and
// the run that publishes them is the run that was given them. A run that
// had to count for itself would be writing arithmetic nobody can check.
//
// How it reaches a run is `grain mcpserver -grant release-notes`, passed
// by a Framework only for a task holding this grant (agent.RunConfig.
// Grants, agent.GrantArgs), and only for a run that also has a route
// back to the daemon -- both the counting and the write happen in the
// daemon's store, so there is nothing a sandbox with no daemon to ask
// could do with either.
package releasenotes

import "github.com/ai-on-gke/grain/pkg/model"

// CapabilityName is the grant name ui.OfferedCapabilities lists as
// "release-notes".
const CapabilityName = "release-notes"

// Provider is the release-notes capability. Every method but Spec is
// model.BaseCapability's default: there is no credential to withhold, no
// lease to mint, nothing to place and nothing to revoke.
type Provider struct {
	model.BaseCapability
}

// New returns the provider, for cmd/grain's registry.
func New() Provider { return Provider{} }

// Spec is the capability's identity. GRANT and no Requires: this
// capability holds no credential of its own, and the run it serves holds
// none either -- it asks the daemon, which is already the only thing
// that writes this deployment's store.
func (p Provider) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{
		Name:      CapabilityName,
		Label:     "Release notes",
		Source:    model.GrantByLabel,
		Provision: model.ProvisionGrant,
		Description: "Let this task read how much work this deployment has done over a " +
			"window -- pull requests merged, tasks completed, runs, and the same " +
			"counts per repository -- and publish a week's release notes, which are " +
			"shown in the UI and exported to the state repository as markdown",
	}
}
