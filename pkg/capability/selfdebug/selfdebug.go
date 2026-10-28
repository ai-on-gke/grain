// Package selfdebug is the "self-debug" capability: when a task holds it,
// grain places its own baked-in source checkout read-only at grain-src/
// in the sandbox, grants the api:self-debug preset (api:tasks:read and
// api:deployment:read), and registers pkg/mcp's NewTaskTools and
// NewMetricsTools on `grain mcpserver -grant self-debug`.
//
// It mints nothing and writes no Placement -- Resolve, Materialize,
// PromptSection and Revoke are all model.BaseCapability's defaults --
// because what it grants is not credential material, it is the read-only
// grain-src/ checkout and the task, metrics and deployment inspection
// tools.
//
// See pkg/capability/selfrepair for this capability's destructive
// counterpart, which does need a confirmation step.
package selfdebug

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ai-on-gke/grain/pkg/model"
)

// CapabilityName is the grant name ui.OfferedCapabilities lists as
// "self-debug".
const CapabilityName = model.SelfDebugCapability

// Provider is the self-debug capability. Every method but Spec is
// model.BaseCapability's default: Resolve always honours (there is no
// credential to withhold), and Materialize/PromptSection/Revoke all do
// nothing, since nothing here mints a lease or places a credential file.
type Provider struct {
	model.BaseCapability
}

// Config holds the configuration for selfdebug Provider.
type Config struct{}

// New returns a self-debug Provider, ready to register.
func New() *Provider { return &Provider{} }

// NewWithConfig returns a self-debug Provider configured with cfg.
func NewWithConfig(_ Config) *Provider { return &Provider{} }

func (p *Provider) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{
		Name:        CapabilityName,
		Label:       "Self debug",
		Description: "Let this task read grain's own source checkout, its tasks and runs, and the measurements grain keeps about itself -- throughput, latency, the tool-use census -- to help it debug or explain grain's own behavior. It also grants the api:self-debug preset (api:tasks:read and api:deployment:read): every task's record, conversation and transcript, and this deployment's host, sandbox, upgrade and metric status and its redacted logs. All read-only.",
		Source:      model.GrantByLabel,
		Provision:   model.ProvisionGrant,
	}
}

// CheckSourceReadable reports whether this process's own account can
// actually read srcDir -- not merely whether the directory is there, and
// not merely whether its root lists.
//
// A deployment that has no source checkout at all (srcDir == "") has
// nothing to check and returns nil.
func CheckSourceReadable(srcDir string) error {
	if srcDir == "" {
		return nil
	}
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return fmt.Errorf("grain's own source checkout at %s cannot be listed by this process (uid %d): %w -- "+
			"the self-debug capability cannot stage grain-src/",
			srcDir, os.Geteuid(), err)
	}
	// One directory below the root, and one file in it: the cheapest pair
	// that distinguishes "staged readable" from "the root is the only
	// readable thing in it". Which entry is immaterial, so this takes the
	// first that answers either way.
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		below := filepath.Join(srcDir, entry.Name())
		if _, err := os.ReadDir(below); err != nil {
			return fmt.Errorf("grain's own source checkout at %s lists at its root but %s cannot be read by "+
				"this process (uid %d): %w -- the self-debug capability can read nothing below that root. "+
				"The tree was staged with modes this account cannot use (scripts/export-source.sh, and "+
				"setup.sh's extract-host-binaries.sh)", srcDir, below, os.Geteuid(), err)
		}
		return nil
	}
	return nil
}
