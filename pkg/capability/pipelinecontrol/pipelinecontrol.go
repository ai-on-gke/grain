package pipelinecontrol

import (
	"context"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// CapabilityName is the grant name: "pipeline-control".
const CapabilityName = "pipeline-control"

// Controller is what pause_pipeline and resume_pipeline call to pause or
// resume dispatch. Each reports the team it paused or resumed, or "" for
// the whole deployment -- see mcp.PipelineController.
type Controller interface {
	PausePipeline(ctx context.Context, repo, reason string) (team string, err error)
	ResumePipeline(ctx context.Context, repo, reason string) (team string, err error)
}

// Config configures the tools pipeline-control provides.
type Config struct {
	TaskID     string
	Controller Controller
}

// Provider implements model.CapabilityProvider for pipeline-control.
type Provider struct {
	model.BaseCapability
}

// ProviderConfig holds configuration for pipelinecontrol Provider.
type ProviderConfig struct{}

// New returns a pipeline-control Provider ready to register.
func New() *Provider { return &Provider{} }

// NewWithConfig returns a pipeline-control Provider with config.
func NewWithConfig(_ ProviderConfig) *Provider { return &Provider{} }

func (p *Provider) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{
		Name:        CapabilityName,
		Label:       "Pipeline control",
		Source:      model.GrantByLabel,
		Provision:   model.ProvisionGrant,
		Description: "Emergency controls to pause or resume dispatch for the whole deployment.",
	}
}

// Tools returns the pause_pipeline and resume_pipeline tools.
func Tools(cfg Config) []mcp.Tool {
	return mcp.NewPipelineControlTools(cfg.Controller)
}
