package pipelinecontrol

import (
	"context"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
)

func TestProviderSpec(t *testing.T) {
	p := New()
	spec := p.Spec()
	if spec.Name != CapabilityName {
		t.Fatalf("spec.Name = %q, want %q", spec.Name, CapabilityName)
	}
	if spec.Label != "Pipeline control" {
		t.Fatalf("spec.Label = %q, want %q", spec.Label, "Pipeline control")
	}
	if spec.Source != model.GrantByLabel {
		t.Fatalf("spec.Source = %v, want %v", spec.Source, model.GrantByLabel)
	}
	if spec.Provision != model.ProvisionGrant {
		t.Fatalf("spec.Provision = %v, want %v", spec.Provision, model.ProvisionGrant)
	}
}

type mockController struct {
	pausedRepo    string
	pausedReason  string
	resumedRepo   string
	resumedReason string
}

func (m *mockController) PausePipeline(ctx context.Context, repo, reason string) (string, error) {
	m.pausedRepo = repo
	m.pausedReason = reason
	return "", nil
}

func (m *mockController) ResumePipeline(ctx context.Context, repo, reason string) (string, error) {
	m.resumedRepo = repo
	m.resumedReason = reason
	return "", nil
}

func TestTools(t *testing.T) {
	ctrl := &mockController{}
	tools := Tools(Config{
		TaskID:     "task-123",
		Controller: ctrl,
	})
	if len(tools) != 2 {
		t.Fatalf("len(tools) = %d, want 2", len(tools))
	}
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
	}
	if !names["pause_pipeline"] || !names["resume_pipeline"] {
		t.Fatalf("tools missing pause_pipeline or resume_pipeline: %v", names)
	}
}
